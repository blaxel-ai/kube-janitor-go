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
	dynamicClient.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil
	})

	j := &Janitor{
		DynamicClient: dynamicClient,
		Config: Config{
			DryRun: false,
		},
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
	dynamicClient.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteCalled = true
		return true, nil, nil
	})

	j := &Janitor{
		DynamicClient: dynamicClient,
		Config: Config{
			DryRun: true,
		},
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
