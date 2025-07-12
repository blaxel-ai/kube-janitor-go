package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/blaxel-ai/kube-janitor-go/internal/janitor"
	"github.com/blaxel-ai/kube-janitor-go/internal/rules"
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

// TestIntegrationResourceFiltering tests resource filtering
func TestIntegrationResourceFiltering(t *testing.T) {
	tests := []struct {
		name              string
		includeResources  []string
		excludeResources  []string
		includeNamespaces []string
		excludeNamespaces []string
		resource          string
		namespace         string
		shouldProcess     bool
	}{
		{
			name:              "include all resources and namespaces",
			includeResources:  []string{},
			excludeResources:  []string{},
			includeNamespaces: []string{},
			excludeNamespaces: []string{},
			resource:          "pods",
			namespace:         "default",
			shouldProcess:     true,
		},
		{
			name:              "exclude specific resource",
			includeResources:  []string{},
			excludeResources:  []string{"pods"},
			includeNamespaces: []string{},
			excludeNamespaces: []string{},
			resource:          "pods",
			namespace:         "default",
			shouldProcess:     false,
		},
		{
			name:              "exclude specific namespace",
			includeResources:  []string{},
			excludeResources:  []string{},
			includeNamespaces: []string{},
			excludeNamespaces: []string{"kube-system"},
			resource:          "pods",
			namespace:         "kube-system",
			shouldProcess:     false,
		},
		{
			name:              "include specific resources only",
			includeResources:  []string{"deployments", "statefulsets"},
			excludeResources:  []string{},
			includeNamespaces: []string{},
			excludeNamespaces: []string{},
			resource:          "pods",
			namespace:         "default",
			shouldProcess:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := janitor.NewResourceFilter(
				tt.includeResources,
				tt.excludeResources,
				tt.includeNamespaces,
				tt.excludeNamespaces,
			)

			// Test resource filtering
			resourceOk := rf.ShouldProcessResource(tt.resource)
			namespaceOk := rf.ShouldProcessNamespace(tt.namespace)
			shouldProcess := resourceOk && namespaceOk

			assert.Equal(t, tt.shouldProcess, shouldProcess,
				"Resource %s in namespace %s processing mismatch", tt.resource, tt.namespace)
		})
	}
}

// TestIntegrationTTLDeletion tests TTL-based deletion
func TestIntegrationTTLDeletion(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create test pod with expired TTL
	expiredPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name":      "expired-pod",
				"namespace": "default",
				"uid":       "uid-1",
				"annotations": map[string]interface{}{
					"janitor/ttl": "1h",
				},
				"creationTimestamp": now.Add(-2 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClient(scheme, expiredPod)

	// Track delete calls using a channel for synchronization
	deleteChan := make(chan bool, 1)
	dynamicClient.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(ktesting.DeleteAction)
		if deleteAction.GetName() == "expired-pod" {
			select {
			case deleteChan <- true:
			default:
				// Channel is full, deletion already signaled
			}
		}
		return true, nil, nil
	})

	// Create minimal janitor config
	config := janitor.Config{
		DryRun:     false,
		MaxWorkers: 1,
	}

	// Create janitor instance
	j := &janitor.Janitor{
		Clientset:     k8sfake.NewSimpleClientset(),
		DynamicClient: dynamicClient,
		Config:        config,
		WorkQueue:     make(chan janitor.WorkItem, 10),
		ResourceFilter: janitor.NewResourceFilter(
			[]string{}, []string{},
			[]string{}, []string{},
		),
	}

	// Process the expired pod
	item := janitor.WorkItem{
		Resource: schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "pods",
		},
		Namespace: "default",
		Name:      "expired-pod",
		Obj:       expiredPod,
	}

	// Start a worker
	go j.Worker(ctx)

	// Send item to work queue
	j.WorkQueue <- item

	// Wait for deletion with timeout
	select {
	case <-deleteChan:
		// Success: deletion occurred
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for pod deletion")
	}
}

// TestIntegrationRulesEngine tests the rules engine
func TestIntegrationRulesEngine(t *testing.T) {
	// Create test rules
	testRules := []rules.Rule{
		{
			ID:         "require-app-label",
			Resources:  []string{"deployments"},
			Expression: "!has(object.spec.template.metadata.labels.app)",
			TTL:        "24h",
		},
		{
			ID:         "cleanup-pr-resources",
			Resources:  []string{"*"},
			Expression: `object.metadata.name.startsWith("pr-")`,
			TTL:        "4h",
		},
	}

	engine, err := rules.New(testRules)
	require.NoError(t, err)

	// Test deployment without app label
	deploymentNoLabel := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "Deployment",
			"metadata": map[string]interface{}{
				"name": "test-deployment",
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"tier": "backend",
						},
					},
				},
			},
		},
	}

	rule, ttl := engine.Evaluate(deploymentNoLabel)
	assert.NotNil(t, rule)
	assert.Equal(t, "require-app-label", rule.ID)
	assert.Equal(t, 24*time.Hour, ttl)

	// Test PR resource
	prPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "Pod",
			"metadata": map[string]interface{}{
				"name": "pr-123-test",
			},
		},
	}

	rule, ttl = engine.Evaluate(prPod)
	assert.NotNil(t, rule)
	assert.Equal(t, "cleanup-pr-resources", rule.ID)
	assert.Equal(t, 4*time.Hour, ttl)

	// Test resource that doesn't match any rule
	normalPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "Pod",
			"metadata": map[string]interface{}{
				"name": "normal-pod",
			},
		},
	}

	rule, ttl = engine.Evaluate(normalPod)
	assert.Nil(t, rule)
	assert.Equal(t, time.Duration(0), ttl)
}

// TestIntegrationNamespaceHandling tests namespace handling
func TestIntegrationNamespaceHandling(t *testing.T) {
	ctx := context.Background()

	// Create fake clientset with namespaces
	clientset := k8sfake.NewSimpleClientset()

	// Create test namespaces
	testNamespaces := []string{"default", "production", "staging", "kube-system"}
	for _, ns := range testNamespaces {
		_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Create janitor with namespace filtering
	config := janitor.Config{
		IncludeNamespaces: []string{},
		ExcludeNamespaces: []string{"kube-system", "production"},
	}

	j := &janitor.Janitor{
		Clientset: clientset,
		Config:    config,
		ResourceFilter: janitor.NewResourceFilter(
			config.IncludeResources,
			config.ExcludeResources,
			config.IncludeNamespaces,
			config.ExcludeNamespaces,
		),
	}

	// Get namespaces
	namespaces, err := j.GetNamespaces(ctx)
	require.NoError(t, err)

	// Filter namespaces
	var filteredNamespaces []string
	for _, ns := range namespaces {
		if j.ResourceFilter.ShouldProcessNamespace(ns) {
			filteredNamespaces = append(filteredNamespaces, ns)
		}
	}

	// Verify filtering
	assert.Contains(t, filteredNamespaces, "default")
	assert.Contains(t, filteredNamespaces, "staging")
	assert.NotContains(t, filteredNamespaces, "kube-system")
	assert.NotContains(t, filteredNamespaces, "production")
}
