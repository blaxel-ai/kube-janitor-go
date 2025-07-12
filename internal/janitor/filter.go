package janitor

// ResourceFilter handles filtering of resources and namespaces
type ResourceFilter struct {
	includeResources     map[string]bool
	excludeResources     map[string]bool
	includeNamespaces    map[string]bool
	excludeNamespaces    map[string]bool
	includeAllResources  bool
	includeAllNamespaces bool
}

// NewResourceFilter creates a new ResourceFilter
func NewResourceFilter(includeResources, excludeResources, includeNamespaces, excludeNamespaces []string) *ResourceFilter {
	rf := &ResourceFilter{
		includeResources:  make(map[string]bool),
		excludeResources:  make(map[string]bool),
		includeNamespaces: make(map[string]bool),
		excludeNamespaces: make(map[string]bool),
	}

	// Process include resources
	if len(includeResources) == 0 {
		rf.includeAllResources = true
	} else {
		for _, r := range includeResources {
			rf.includeResources[r] = true
		}
	}

	// Process exclude resources
	for _, r := range excludeResources {
		rf.excludeResources[r] = true
	}

	// Process include namespaces
	if len(includeNamespaces) == 0 {
		rf.includeAllNamespaces = true
	} else {
		for _, ns := range includeNamespaces {
			rf.includeNamespaces[ns] = true
		}
	}

	// Process exclude namespaces
	for _, ns := range excludeNamespaces {
		rf.excludeNamespaces[ns] = true
	}

	return rf
}

// ShouldProcessResource checks if a resource should be processed
func (rf *ResourceFilter) ShouldProcessResource(resource string) bool {
	// Check excludes first
	if rf.excludeResources[resource] {
		return false
	}

	// Check includes
	if rf.includeAllResources {
		return true
	}

	return rf.includeResources[resource]
}

// ShouldProcessNamespace checks if a namespace should be processed
func (rf *ResourceFilter) ShouldProcessNamespace(namespace string) bool {
	// Check excludes first
	if rf.excludeNamespaces[namespace] {
		return false
	}

	// Check includes
	if rf.includeAllNamespaces {
		return true
	}

	return rf.includeNamespaces[namespace]
}
