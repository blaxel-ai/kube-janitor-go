package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNewRulesEngine(t *testing.T) {
	tests := []struct {
		name      string
		rules     []Rule
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid rules",
			rules: []Rule{
				{
					ID:         "test-rule",
					Resources:  []string{"pods"},
					Expression: "true",
					TTL:        "1h",
				},
			},
			wantError: false,
		},
		{
			name: "invalid rule ID",
			rules: []Rule{
				{
					ID:         "Test-Rule", // uppercase not allowed
					Resources:  []string{"pods"},
					Expression: "true",
					TTL:        "1h",
				},
			},
			wantError: true,
			errorMsg:  "invalid rule ID",
		},
		{
			name: "invalid TTL",
			rules: []Rule{
				{
					ID:         "test-rule",
					Resources:  []string{"pods"},
					Expression: "true",
					TTL:        "invalid",
				},
			},
			wantError: true,
			errorMsg:  "invalid TTL",
		},
		{
			name: "invalid expression",
			rules: []Rule{
				{
					ID:         "test-rule",
					Resources:  []string{"pods"},
					Expression: "this is not valid CEL",
					TTL:        "1h",
				},
			},
			wantError: true,
			errorMsg:  "failed to compile expression",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := New(tt.rules)
			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, engine)
			}
		})
	}
}

func TestEvaluateRule(t *testing.T) {
	rules := []Rule{
		{
			ID:         "no-app-label",
			Resources:  []string{"deployments"},
			Expression: "!has(object.spec.template.metadata.labels.app)",
			TTL:        "1h",
		},
		{
			ID:         "pr-deployments",
			Resources:  []string{"deployments"},
			Expression: `object.metadata.name.startsWith("pr-")`,
			TTL:        "30m",
		},
		{
			ID:         "all-resources",
			Resources:  []string{"*"},
			Expression: "has(object.metadata.labels.cleanup) && object.metadata.labels.cleanup == 'true'",
			TTL:        "10m",
		},
	}

	engine, err := New(rules)
	require.NoError(t, err)

	tests := []struct {
		name         string
		obj          *unstructured.Unstructured
		wantRuleID   string
		wantMatch    bool
		wantDuration time.Duration
	}{
		{
			name: "deployment without app label",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind": "Deployment",
					"metadata": map[string]interface{}{
						"name": "test-deployment",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"tier": "frontend",
								},
							},
						},
					},
				},
			},
			wantRuleID:   "no-app-label",
			wantMatch:    true,
			wantDuration: 1 * time.Hour,
		},
		{
			name: "deployment with app label",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind": "Deployment",
					"metadata": map[string]interface{}{
						"name": "test-deployment",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"app": "my-app",
								},
							},
						},
					},
				},
			},
			wantMatch: false,
		},
		{
			name: "pr deployment",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind": "Deployment",
					"metadata": map[string]interface{}{
						"name": "pr-123-deployment",
					},
				},
			},
			wantRuleID:   "pr-deployments",
			wantMatch:    true,
			wantDuration: 30 * time.Minute,
		},
		{
			name: "pod with cleanup label",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind": "Pod",
					"metadata": map[string]interface{}{
						"name": "test-pod",
						"labels": map[string]interface{}{
							"cleanup": "true",
						},
					},
				},
			},
			wantRuleID:   "all-resources",
			wantMatch:    true,
			wantDuration: 10 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, duration := engine.Evaluate(tt.obj)
			if tt.wantMatch {
				require.NotNil(t, rule)
				assert.Equal(t, tt.wantRuleID, rule.ID)
				assert.Equal(t, tt.wantDuration, duration)
			} else {
				assert.Nil(t, rule)
				assert.Equal(t, time.Duration(0), duration)
			}
		})
	}
}

func TestResourceMatches(t *testing.T) {
	engine := &Engine{}

	tests := []struct {
		name      string
		resources []string
		kind      string
		want      bool
	}{
		{
			name:      "wildcard match",
			resources: []string{"*"},
			kind:      "Pod",
			want:      true,
		},
		{
			name:      "exact match",
			resources: []string{"Pod"},
			kind:      "Pod",
			want:      true,
		},
		{
			name:      "plural match",
			resources: []string{"pods"},
			kind:      "Pod",
			want:      true,
		},
		{
			name:      "no match",
			resources: []string{"deployments"},
			kind:      "Pod",
			want:      false,
		},
		{
			name:      "multiple resources with match",
			resources: []string{"services", "pods", "deployments"},
			kind:      "Pod",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.resourceMatches(tt.resources, tt.kind)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPluralize(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"Pod", "pods"},
		{"Service", "services"},
		{"Deployment", "deployments"},
		{"StatefulSet", "statefulsets"},
		{"DaemonSet", "daemonsets"},
		{"ReplicaSet", "replicasets"},
		{"ConfigMap", "configmaps"},
		{"Secret", "secrets"},
		{"PersistentVolumeClaim", "persistentvolumeclaims"},
		{"PersistentVolume", "persistentvolumes"},
		{"Namespace", "namespaces"},
		{"Ingress", "ingresses"},
		{"NetworkPolicy", "networkpolicies"},
		{"CustomResource", "CustomResources"}, // default case
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got := pluralize(tt.kind)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temporary rules file
	tmpDir := t.TempDir()
	rulesFile := filepath.Join(tmpDir, "rules.yaml")

	rulesContent := `rules:
  - id: test-rule
    resources:
      - pods
    expression: "true"
    ttl: 1h
  - id: another-rule
    resources:
      - deployments
    expression: "object.metadata.name == 'test'"
    ttl: 30m`

	err := os.WriteFile(rulesFile, []byte(rulesContent), 0600)
	require.NoError(t, err)

	// Test loading from file
	engine, err := LoadFromFile(rulesFile)
	require.NoError(t, err)
	assert.NotNil(t, engine)
	assert.Len(t, engine.rules, 2)

	// Test non-existent file
	_, err = LoadFromFile("/non/existent/file.yaml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read rules file")

	// Test invalid YAML
	invalidFile := filepath.Join(tmpDir, "invalid.yaml")
	err = os.WriteFile(invalidFile, []byte("invalid: yaml: content"), 0600)
	require.NoError(t, err)

	_, err = LoadFromFile(invalidFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse rules file")
}
