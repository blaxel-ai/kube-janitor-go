package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/blaxel-ai/kube-janitor-go/internal/metrics"
	"github.com/blaxel-ai/kube-janitor-go/internal/rules"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
	ruleEngine, err := rules.New([]rules.Rule{
		{
			ID:         "cleanup-test-pods",
			Resources:  []string{"pods"},
			Expression: `object.metadata.name == "rule-pod"`,
			TTL:        "1h",
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name                string
		obj                 *unstructured.Unstructured
		ruleEngine          *rules.Engine
		wantDelete          bool
		wantReason          string
		wantDetailedMessage string
		wantTTLDeadline     bool
	}{
		{
			name: "Delete-if-max-age expired",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "test-pod",
						"creationTimestamp": now.Add(-2 * time.Hour).Format(time.RFC3339),
						"annotations": map[string]interface{}{
							annotationDeleteIfMaxAge: "1h",
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfMaxAge,
			wantDetailedMessage: "Delete max-age policy triggered",
			wantTTLDeadline:     true,
		},
		{
			name: "Delete-if-idle expired",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationDeleteIfIdle: "30m",
							annotationLastUsedAt:   now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfIdle,
			wantDetailedMessage: "Delete idle policy triggered",
			wantTTLDeadline:     true,
		},
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
			wantDelete:          true,
			wantReason:          deletionReasonLegacyTTL,
			wantDetailedMessage: "TTL expired",
			wantTTLDeadline:     true,
		},
		{
			name: "Rule TTL expired",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind": "Pod",
					"metadata": map[string]interface{}{
						"name":              "rule-pod",
						"creationTimestamp": now.Add(-2 * time.Hour).Format(time.RFC3339),
					},
				},
			},
			ruleEngine:          ruleEngine,
			wantDelete:          true,
			wantReason:          deletionReasonRuleTTL,
			wantDetailedMessage: "Rule 'cleanup-test-pods' matched",
			wantTTLDeadline:     true,
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
			wantDelete:          true,
			wantReason:          deletionReasonLegacyExpires,
			wantDetailedMessage: "Legacy expiration time reached",
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
			name: "Delete-if-date time reached",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationDeleteIfDate: now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfDate,
			wantDetailedMessage: "Delete date policy triggered",
		},
		{
			name: "Delete-if-date time not reached",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationDeleteIfDate: now.Add(1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete: false,
		},
		{
			name: "Delete-if-date numbered annotation",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationDeleteIfDate + "-1": now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfDate,
			wantDetailedMessage: "Delete date policy triggered",
		},
		{
			name: "Delete-if-date with -0 suffix",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationDeleteIfDate + "-0": now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfDate,
			wantDetailedMessage: "Delete date policy triggered",
		},
		{
			name: "Delete-if-date-0 with exact format from user",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							"janitor/delete-if-date-0": now.Add(1 * time.Hour).UTC().Format("2000-01-00T12:34:56.000000Z"),
						},
					},
				},
			},
			wantDelete: false,
		},
		{
			name: "Delete-if-date-0 with past date",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							"janitor/delete-if-date-0": now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete:          true,
			wantReason:          deletionReasonDeleteIfDate,
			wantDetailedMessage: "Delete date policy triggered",
		},
		{
			name: "Archive-if-date annotation (mock - should not delete)",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"annotations": map[string]interface{}{
							annotationArchiveIfDate: now.Add(-1 * time.Hour).Format(time.RFC3339),
						},
					},
				},
			},
			wantDelete: false, // Archive functionality is not implemented yet
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
			j := &Janitor{RuleEngine: tt.ruleEngine}
			gotDecision := j.shouldDelete(tt.obj)
			assert.Equal(t, tt.wantDelete, gotDecision.shouldDelete)
			if tt.wantDelete && tt.wantReason != "" {
				assert.Equal(t, tt.wantReason, gotDecision.reason)
				assert.Contains(t, gotDecision.detailedMessage, tt.wantDetailedMessage)
				assert.NotContains(t, gotDecision.detailedMessage, gotDecision.reason,
					"bounded metric reason should not be reused as the detailed human-readable message")
				assert.Equal(t, tt.wantTTLDeadline, !gotDecision.ttlDeadline.IsZero())
			} else {
				assert.Empty(t, gotDecision.reason)
				assert.Empty(t, gotDecision.detailedMessage)
				assert.True(t, gotDecision.ttlDeadline.IsZero())
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
				"uid":       "test-pod-uid",
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

	ttlLagSamplesBefore := ttlDeletionLagSampleCount(t, item.Resource.Resource, deletionReasonLegacyTTL)
	j.processItem(ctx, item)
	assert.True(t, deleteCalled, "Delete should have been called")
	assert.Equal(t, ttlLagSamplesBefore+1, ttlDeletionLagSampleCount(t, item.Resource.Resource, deletionReasonLegacyTTL))
}

func TestDeleteOptionsForObjectUsesUIDPrecondition(t *testing.T) {
	pod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "test-pod",
				"uid":  "test-pod-uid",
			},
		},
	}

	deleteOptions := deleteOptionsForObject(pod)

	require.NotNil(t, deleteOptions.Preconditions)
	require.NotNil(t, deleteOptions.Preconditions.UID)
	assert.Equal(t, pod.GetUID(), *deleteOptions.Preconditions.UID)
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

	ttlLagSamplesBefore := ttlDeletionLagSampleCount(t, item.Resource.Resource, deletionReasonLegacyTTL)
	j.processItem(ctx, item)
	assert.False(t, deleteCalled, "Delete should not have been called in dry-run mode")
	assert.Equal(t, ttlLagSamplesBefore, ttlDeletionLagSampleCount(t, item.Resource.Resource, deletionReasonLegacyTTL))
}

func ttlDeletionLagSampleCount(t *testing.T, resource, reason string) uint64 {
	t.Helper()

	collected := make(chan prometheus.Metric)
	go func() {
		metrics.TTLDeletionLag.Collect(collected)
		close(collected)
	}()

	var count uint64
	for metric := range collected {
		var dtoMetric dto.Metric
		require.NoError(t, metric.Write(&dtoMetric))
		if metricHasLabels(&dtoMetric, map[string]string{
			"resource": resource,
			"reason":   reason,
		}) {
			count += dtoMetric.GetHistogram().GetSampleCount()
		}
	}
	return count
}

func metricHasLabels(metric *dto.Metric, labels map[string]string) bool {
	matched := 0
	for _, label := range metric.GetLabel() {
		want, ok := labels[label.GetName()]
		if ok && label.GetValue() == want {
			matched++
		}
	}
	return matched == len(labels)
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

func TestEvaluateDeleteIfDate(t *testing.T) {
	j := &Janitor{}
	now := time.Now()

	tests := []struct {
		name       string
		value      string
		wantDelete bool
		wantReason string
		wantError  bool
	}{
		{
			name:       "Date in past (should delete)",
			value:      now.Add(-1 * time.Hour).Format(time.RFC3339),
			wantDelete: true,
			wantReason: "Delete date policy triggered",
			wantError:  false,
		},
		{
			name:       "Date in future (should not delete)",
			value:      now.Add(1 * time.Hour).Format(time.RFC3339),
			wantDelete: false,
			wantError:  false,
		},
		{
			name:       "Date only format (should delete)",
			value:      now.Add(-24 * time.Hour).Format("2006-01-02"),
			wantDelete: true,
			wantReason: "Delete date policy triggered",
			wantError:  false,
		},
		{
			name:      "Invalid date format",
			value:     "invalid-date",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			gotDelete, gotReason, err := j.evaluateDeleteIfDate(tt.value, obj)

			if tt.wantError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantDelete, gotDelete)

			if tt.wantDelete {
				assert.Contains(t, gotReason, tt.wantReason)
			}
		})
	}
}
