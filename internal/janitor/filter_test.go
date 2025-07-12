package janitor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewResourceFilter(t *testing.T) {
	tests := []struct {
		name              string
		includeResources  []string
		excludeResources  []string
		includeNamespaces []string
		excludeNamespaces []string
		wantIncludeAllRes bool
		wantIncludeAllNs  bool
	}{
		{
			name:              "empty includes means include all",
			includeResources:  []string{},
			excludeResources:  []string{"events"},
			includeNamespaces: []string{},
			excludeNamespaces: []string{"kube-system"},
			wantIncludeAllRes: true,
			wantIncludeAllNs:  true,
		},
		{
			name:              "specific includes",
			includeResources:  []string{"pods", "deployments"},
			excludeResources:  []string{},
			includeNamespaces: []string{"default", "test"},
			excludeNamespaces: []string{},
			wantIncludeAllRes: false,
			wantIncludeAllNs:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := NewResourceFilter(tt.includeResources, tt.excludeResources, tt.includeNamespaces, tt.excludeNamespaces)
			assert.Equal(t, tt.wantIncludeAllRes, rf.includeAllResources)
			assert.Equal(t, tt.wantIncludeAllNs, rf.includeAllNamespaces)
		})
	}
}

func TestShouldProcessResource(t *testing.T) {
	tests := []struct {
		name             string
		includeResources []string
		excludeResources []string
		resource         string
		want             bool
	}{
		{
			name:             "include all, no excludes",
			includeResources: []string{},
			excludeResources: []string{},
			resource:         "pods",
			want:             true,
		},
		{
			name:             "include all, with excludes",
			includeResources: []string{},
			excludeResources: []string{"events", "controllerrevisions"},
			resource:         "events",
			want:             false,
		},
		{
			name:             "include all, resource not excluded",
			includeResources: []string{},
			excludeResources: []string{"events", "controllerrevisions"},
			resource:         "pods",
			want:             true,
		},
		{
			name:             "specific includes, resource included",
			includeResources: []string{"pods", "deployments"},
			excludeResources: []string{},
			resource:         "pods",
			want:             true,
		},
		{
			name:             "specific includes, resource not included",
			includeResources: []string{"pods", "deployments"},
			excludeResources: []string{},
			resource:         "services",
			want:             false,
		},
		{
			name:             "resource included but also excluded",
			includeResources: []string{"pods", "deployments"},
			excludeResources: []string{"pods"},
			resource:         "pods",
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := NewResourceFilter(tt.includeResources, tt.excludeResources, []string{}, []string{})
			got := rf.ShouldProcessResource(tt.resource)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShouldProcessNamespace(t *testing.T) {
	tests := []struct {
		name              string
		includeNamespaces []string
		excludeNamespaces []string
		namespace         string
		want              bool
	}{
		{
			name:              "include all, no excludes",
			includeNamespaces: []string{},
			excludeNamespaces: []string{},
			namespace:         "default",
			want:              true,
		},
		{
			name:              "include all, with excludes",
			includeNamespaces: []string{},
			excludeNamespaces: []string{"kube-system", "kube-public"},
			namespace:         "kube-system",
			want:              false,
		},
		{
			name:              "include all, namespace not excluded",
			includeNamespaces: []string{},
			excludeNamespaces: []string{"kube-system", "kube-public"},
			namespace:         "default",
			want:              true,
		},
		{
			name:              "specific includes, namespace included",
			includeNamespaces: []string{"default", "test"},
			excludeNamespaces: []string{},
			namespace:         "default",
			want:              true,
		},
		{
			name:              "specific includes, namespace not included",
			includeNamespaces: []string{"default", "test"},
			excludeNamespaces: []string{},
			namespace:         "production",
			want:              false,
		},
		{
			name:              "namespace included but also excluded",
			includeNamespaces: []string{"default", "test"},
			excludeNamespaces: []string{"test"},
			namespace:         "test",
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := NewResourceFilter([]string{}, []string{}, tt.includeNamespaces, tt.excludeNamespaces)
			got := rf.ShouldProcessNamespace(tt.namespace)
			assert.Equal(t, tt.want, got)
		})
	}
}
