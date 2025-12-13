package sharding

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"

	"github.com/sirupsen/logrus"
)

const (
	// DefaultVirtualNodes is the default number of virtual nodes per physical node
	DefaultVirtualNodes = 150
)

// HashRing implements consistent hashing with virtual nodes for distributing
// resources across multiple janitor instances.
type HashRing struct {
	ring         []uint64          // sorted hash values
	nodes        map[uint64]string // hash -> node name
	virtualNodes int
	selfName     string
	mu           sync.RWMutex
}

// NewHashRing creates a new HashRing instance.
// selfName is the identifier of this janitor instance (typically the pod name).
// virtualNodes controls the distribution granularity (higher = more even distribution).
func NewHashRing(selfName string, virtualNodes int) *HashRing {
	if virtualNodes <= 0 {
		virtualNodes = DefaultVirtualNodes
	}
	return &HashRing{
		ring:         make([]uint64, 0),
		nodes:        make(map[uint64]string),
		virtualNodes: virtualNodes,
		selfName:     selfName,
	}
}

// fnv1aHash computes FNV-1a hash of a string
func fnv1aHash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return h.Sum64()
}

// AddNode adds a node to the hash ring with virtual nodes for better distribution
func (h *HashRing) AddNode(nodeName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := 0; i < h.virtualNodes; i++ {
		virtualKey := virtualNodeKey(nodeName, i)
		hash := fnv1aHash(virtualKey)
		h.ring = append(h.ring, hash)
		h.nodes[hash] = nodeName
	}

	// Keep ring sorted for binary search
	sort.Slice(h.ring, func(i, j int) bool {
		return h.ring[i] < h.ring[j]
	})

	logrus.WithFields(logrus.Fields{
		"node":         nodeName,
		"virtualNodes": h.virtualNodes,
		"totalNodes":   len(h.ring),
	}).Debug("Added node to hash ring")
}

// RemoveNode removes a node and its virtual nodes from the hash ring
func (h *HashRing) RemoveNode(nodeName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find and remove all virtual nodes for this node
	newRing := make([]uint64, 0, len(h.ring))
	for _, hash := range h.ring {
		if h.nodes[hash] != nodeName {
			newRing = append(newRing, hash)
		} else {
			delete(h.nodes, hash)
		}
	}
	h.ring = newRing

	logrus.WithFields(logrus.Fields{
		"node":       nodeName,
		"totalNodes": len(h.ring),
	}).Debug("Removed node from hash ring")
}

// SetNodes replaces all nodes in the hash ring with the provided list
func (h *HashRing) SetNodes(nodeNames []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Clear existing ring
	h.ring = make([]uint64, 0, len(nodeNames)*h.virtualNodes)
	h.nodes = make(map[uint64]string)

	// Add all nodes
	for _, nodeName := range nodeNames {
		for i := 0; i < h.virtualNodes; i++ {
			virtualKey := virtualNodeKey(nodeName, i)
			hash := fnv1aHash(virtualKey)
			h.ring = append(h.ring, hash)
			h.nodes[hash] = nodeName
		}
	}

	// Keep ring sorted for binary search
	sort.Slice(h.ring, func(i, j int) bool {
		return h.ring[i] < h.ring[j]
	})

	logrus.WithFields(logrus.Fields{
		"nodes":        nodeNames,
		"virtualNodes": h.virtualNodes,
		"totalEntries": len(h.ring),
	}).Debug("Set nodes in hash ring")
}

// GetNode returns the node responsible for the given key
func (h *HashRing) GetNode(key string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.ring) == 0 {
		return ""
	}

	hash := fnv1aHash(key)

	// Binary search for the first hash >= key hash
	idx := sort.Search(len(h.ring), func(i int) bool {
		return h.ring[i] >= hash
	})

	// Wrap around if we're past the end
	if idx >= len(h.ring) {
		idx = 0
	}

	return h.nodes[h.ring[idx]]
}

// ShouldProcess returns true if this janitor instance should process
// the resource with the given namespace and name
func (h *HashRing) ShouldProcess(namespace, name string) bool {
	key := buildResourceKey(namespace, name)
	node := h.GetNode(key)

	shouldProcess := node == h.selfName

	logrus.WithFields(logrus.Fields{
		"namespace":     namespace,
		"name":          name,
		"key":           key,
		"assignedNode":  node,
		"selfName":      h.selfName,
		"shouldProcess": shouldProcess,
	}).Trace("Sharding decision for resource")

	return shouldProcess
}

// GetSelfName returns the name of this janitor instance
func (h *HashRing) GetSelfName() string {
	return h.selfName
}

// GetNodeCount returns the number of physical nodes in the ring
func (h *HashRing) GetNodeCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.ring) / h.virtualNodes
}

// GetNodes returns the list of unique physical nodes in the ring
func (h *HashRing) GetNodes() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[string]struct{})
	nodes := make([]string, 0)

	for _, nodeName := range h.nodes {
		if _, exists := seen[nodeName]; !exists {
			seen[nodeName] = struct{}{}
			nodes = append(nodes, nodeName)
		}
	}

	sort.Strings(nodes)
	return nodes
}

// buildResourceKey creates a consistent key from namespace and name
func buildResourceKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// virtualNodeKey generates a unique key for a virtual node
func virtualNodeKey(nodeName string, index int) string {
	return nodeName + "#" + strconv.Itoa(index)
}
