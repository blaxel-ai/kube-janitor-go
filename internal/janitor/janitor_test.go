package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
)

func TestParseExpirationTime(t *testing.T) {
	tests := []struct {
		name      string
		expires   string
		want      time.Time
		wantError bool
	}{
		{
			name:    "RFC3339 format",
			expires: "2024-12-31T23:59:59Z",
			want:    time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:    "Date time without timezone",
			expires: "2024-12-31T23:59:59",
			want:    time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:    "Date time without seconds",
			expires: "2024-12-31T23:59",
			want:    time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
		},
		{
			name:    "Date only format",
			expires: "2024-12-31",
			want:    time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "Invalid format",
			expires:   "invalid-date",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExpirationTime(tt.expires)
			if tt.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShouldDelete(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantDelete bool
		wantReason string
	}{
		{
			name: "TTL expired",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "test-pod",
						"creationTimestamp": now.Add(-2 * time.Hour).Format(time.RFC3339),
						"annotations": map[string]interface{}{
							annotationTTL: "1h",
						},
					},
				},
			},
			wantDelete: true,
			wantReason: "TTL expired",
		},
		{
			name: "TTL not expired",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "test-pod",
						"creationTimestamp": now.Add(-30 * time.Minute).Format(time.RFC3339),
						"annotations": map[string]interface{}{
							annotationTTL: "1h",
						},
					},
				},
			},
			wantDelete: false,
		},
		{
			name: "Invalid TTL format",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "test-pod",
						"creationTimestamp": now.Add(-2 * time.Hour).Format(time.RFC3339),
						"annotations": map[string]interface{}{
							annotationTTL: "invalid",
						},
					},
				},
			},
			wantDelete: false,
		},
		{
			name: "Expiration time reached",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationExpires: now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete: true,
			wantReason: "Expiration time reached",
		},
		{
			name: "Expiration time not reached",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationExpires: now.Add(1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete: false,
		},
		{
			name: "No annotations",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
					},
				},
			},
			wantDelete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &Janitor{}
			gotDelete, gotReason := j.shouldDelete(tt.obj)
			assert.Equal(t, tt.wantDelete, gotDelete)
			if tt.wantDelete && tt.wantReason != "" {
				assert.Contains(t, gotReason, tt.wantReason)
			}
		})
	}
}

func TestProcessItem(t *testing.T) {
	ctx := context.Background()

	// Create test pod
	pod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name":      "test-pod",
				"namespace": "default",
				"annotations": map[string]interface{}{
					annotationTTL: "0s", // Expired immediately
				},
				"creationTimestamp": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClient(scheme, pod)

	// Track delete calls
	var deleteCalled bool
	dynamicClient.PrependReactor("delete", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil
	})

	// Create event recorder
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(func(_ string, _ ...interface{}) {
		// Discard events in tests
	})
	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "kube-janitor-go-test"})

	j := &Janitor{
		DynamicClient: dynamicClient,
		Config: Config{
			DryRun: false,
		},
		EventRecorder: recorder,
	}

	item := WorkItem{
		Resource: schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "pods",
		},
		Namespace: "default",
		Name:      "test-pod",
		Obj:       pod,
	}

	j.processItem(ctx, item)
	assert.True(t, deleteCalled, "Delete should have been called")
}

func TestProcessItemDryRun(t *testing.T) {
	ctx := context.Background()

	// Create test pod
	pod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name":      "test-pod",
				"namespace": "default",
				"annotations": map[string]interface{}{
					annotationTTL: "0s", // Expired immediately
				},
				"creationTimestamp": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClient(scheme, pod)

	// Track delete calls
	var deleteCalled bool
	dynamicClient.PrependReactor("delete", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil
	})

	// Create event recorder
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(func(_ string, _ ...interface{}) {
		// Discard events in tests
	})
	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "kube-janitor-go-test"})

	j := &Janitor{
		DynamicClient: dynamicClient,
		Config: Config{
			DryRun: true,
		},
		EventRecorder: recorder,
	}

	item := WorkItem{
		Resource: schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "pods",
		},
		Namespace: "default",
		Name:      "test-pod",
		Obj:       pod,
	}

	j.processItem(ctx, item)
	assert.False(t, deleteCalled, "Delete should not have been called in dry-run mode")
}

func TestGetNamespaces(t *testing.T) {
	ctx := context.Background()

	// Create fake clientset with namespaces
	clientset := k8sfake.NewSimpleClientset()

	// Create test namespaces
	namespaces := []string{"default", "kube-system", "test-ns"}
	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	j := &Janitor{
		Clientset: clientset,
	}

	got, err := j.getNamespaces(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, namespaces, got)
}

func TestContains(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		item  string
		want  bool
	}{
		{
			name:  "item exists",
			slice: []string{"foo", "bar", "baz"},
			item:  "bar",
			want:  true,
		},
		{
			name:  "item does not exist",
			slice: []string{"foo", "bar", "baz"},
			item:  "qux",
			want:  false,
		},
		{
			name:  "empty slice",
			slice: []string{},
			item:  "foo",
			want:  false,
		},
		{
			name:  "nil slice",
			slice: nil,
			item:  "foo",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.slice, tt.item)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseExtendedDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		wantErr  bool
	}{
		// Standard Go durations (backward compatibility)
		{
			name:     "standard hours",
			input:    "24h",
			expected: 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "standard minutes",
			input:    "30m",
			expected: 30 * time.Minute,
			wantErr:  false,
		},
		{
			name:     "standard combined",
			input:    "1h30m",
			expected: 90 * time.Minute,
			wantErr:  false,
		},
		// Extended durations - days
		{
			name:     "single day",
			input:    "1d",
			expected: 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "multiple days",
			input:    "7d",
			expected: 7 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "fractional days",
			input:    "1.5d",
			expected: 36 * time.Hour,
			wantErr:  false,
		},
		// Extended durations - weeks
		{
			name:     "single week",
			input:    "1w",
			expected: 7 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "multiple weeks",
			input:    "2w",
			expected: 14 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "fractional weeks",
			input:    "0.5w",
			expected: 84 * time.Hour,
			wantErr:  false,
		},
		// Extended durations - months
		{
			name:     "single month",
			input:    "1month",
			expected: 30 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "single month plural",
			input:    "1months",
			expected: 30 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "multiple months",
			input:    "3months",
			expected: 90 * 24 * time.Hour,
			wantErr:  false,
		},
		// Combined durations
		{
			name:     "weeks and days",
			input:    "2w3d",
			expected: 17 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "month week and days",
			input:    "1month1w2d",
			expected: 39 * 24 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "complex combination",
			input:    "1month2w3d12h30m",
			expected: 47*24*time.Hour + 12*time.Hour + 30*time.Minute,
			wantErr:  false,
		},
		{
			name:     "with spaces",
			input:    "2w 3d 12h",
			expected: 17*24*time.Hour + 12*time.Hour,
			wantErr:  false,
		},
		// Error cases
		{
			name:     "invalid format",
			input:    "invalid",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid unit",
			input:    "5x",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid number",
			input:    "abcd",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "mixed invalid",
			input:    "2w3x",
			expected: 0,
			wantErr:  true,
		},
		// Edge cases
		{
			name:     "zero duration",
			input:    "0d",
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "very small duration",
			input:    "1ms",
			expected: time.Millisecond,
			wantErr:  false,
		},
		{
			name:     "microseconds with µ",
			input:    "100µs",
			expected: 100 * time.Microsecond,
			wantErr:  false,
		},
		{
			name:     "microseconds with us",
			input:    "100us",
			expected: 100 * time.Microsecond,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExtendedDuration(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExtendedDuration() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseExtendedDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestParseExtendedDurationRealWorld tests real-world scenarios
func TestParseExtendedDurationRealWorld(t *testing.T) {
	tests := []struct {
		name        string
		annotation  string
		ageInHours  float64
		shouldMatch bool
	}{
		{
			name:        "7 day TTL, 5 day old resource",
			annotation:  "7d",
			ageInHours:  120, // 5 days
			shouldMatch: false,
		},
		{
			name:        "7 day TTL, 8 day old resource",
			annotation:  "7d",
			ageInHours:  192, // 8 days
			shouldMatch: true,
		},
		{
			name:        "2 week TTL, 10 day old resource",
			annotation:  "2w",
			ageInHours:  240, // 10 days
			shouldMatch: false,
		},
		{
			name:        "1 month TTL, 35 day old resource",
			annotation:  "1month",
			ageInHours:  840, // 35 days
			shouldMatch: true,
		},
		{
			name:        "complex TTL, just under limit",
			annotation:  "1w3d12h",
			ageInHours:  251, // Just under 10.5 days
			shouldMatch: false,
		},
		{
			name:        "complex TTL, just over limit",
			annotation:  "1w3d12h",
			ageInHours:  253, // Just over 10.5 days
			shouldMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration, err := ParseExtendedDuration(tt.annotation)
			if err != nil {
				t.Fatalf("Failed to parse duration: %v", err)
			}

			age := time.Duration(tt.ageInHours * float64(time.Hour))
			shouldDelete := age > duration

			if shouldDelete != tt.shouldMatch {
				t.Errorf("Expected shouldDelete=%v for age=%v and TTL=%v, but got %v",
					tt.shouldMatch, age, duration, shouldDelete)
			}
		})
	}
}
