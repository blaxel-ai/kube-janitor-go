package janitor

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// StoreEntry represents a resource with an expiration time
type StoreEntry struct {
	GVR            schema.GroupVersionResource
	Namespace      string
	Name           string
	ExpirationTime time.Time
	Reason         string
}

// Key returns the unique key for this entry
func (e *StoreEntry) Key() string {
	return storeKey(e.GVR.Resource, e.Namespace, e.Name)
}

// IsExpired returns true if the entry has expired
func (e *StoreEntry) IsExpired() bool {
	return time.Now().After(e.ExpirationTime)
}

// storeKey generates a unique key for a resource
func storeKey(resource, namespace, name string) string {
	return resource + "/" + namespace + "/" + name
}

// ExpirationStore is a thread-safe in-memory store for resources with expiration times
type ExpirationStore struct {
	mu      sync.RWMutex
	entries map[string]*StoreEntry
}

// NewExpirationStore creates a new ExpirationStore
func NewExpirationStore() *ExpirationStore {
	return &ExpirationStore{
		entries: make(map[string]*StoreEntry),
	}
}

// Add adds or updates an entry in the store
func (s *ExpirationStore) Add(gvr schema.GroupVersionResource, namespace, name string, expirationTime time.Time, reason string) {
	key := storeKey(gvr.Resource, namespace, name)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = &StoreEntry{
		GVR:            gvr,
		Namespace:      namespace,
		Name:           name,
		ExpirationTime: expirationTime,
		Reason:         reason,
	}
}

// Remove removes an entry from the store
func (s *ExpirationStore) Remove(gvr schema.GroupVersionResource, namespace, name string) {
	key := storeKey(gvr.Resource, namespace, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// RemoveByKey removes an entry by its key
func (s *ExpirationStore) RemoveByKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Get returns an entry by resource, namespace, and name
func (s *ExpirationStore) Get(gvr schema.GroupVersionResource, namespace, name string) (*StoreEntry, bool) {
	key := storeKey(gvr.Resource, namespace, name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[key]
	return entry, ok
}

// Has returns true if an entry exists for the given resource
func (s *ExpirationStore) Has(gvr schema.GroupVersionResource, namespace, name string) bool {
	key := storeKey(gvr.Resource, namespace, name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[key]
	return ok
}

// GetExpired returns all entries that have expired
// Returns a slice to avoid holding the lock during iteration
func (s *ExpirationStore) GetExpired() []*StoreEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	var expired []*StoreEntry
	for _, entry := range s.entries {
		if now.After(entry.ExpirationTime) {
			// Copy the entry to avoid race conditions
			entryCopy := *entry
			expired = append(expired, &entryCopy)
		}
	}
	return expired
}

// GetAll returns all entries in the store
func (s *ExpirationStore) GetAll() []*StoreEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]*StoreEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entryCopy := *entry
		entries = append(entries, &entryCopy)
	}
	return entries
}

// Len returns the number of entries in the store
func (s *ExpirationStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear removes all entries from the store
func (s *ExpirationStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]*StoreEntry)
}

// GetExpiringSoon returns entries that will expire within the given duration
func (s *ExpirationStore) GetExpiringSoon(within time.Duration) []*StoreEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deadline := time.Now().Add(within)
	var expiring []*StoreEntry
	for _, entry := range s.entries {
		if entry.ExpirationTime.Before(deadline) {
			entryCopy := *entry
			expiring = append(expiring, &entryCopy)
		}
	}
	return expiring
}
