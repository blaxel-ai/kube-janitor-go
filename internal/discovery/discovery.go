package discovery

import (
	"context"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// DefaultRefreshInterval is the default interval for refreshing peer list
	DefaultRefreshInterval = 30 * time.Second
	// DefaultDNSTimeout is the timeout for DNS lookups
	DefaultDNSTimeout = 5 * time.Second
)

// PeerDiscovery handles discovering peer janitor instances via DNS
// using a Kubernetes headless service, or uses static peers if configured.
type PeerDiscovery struct {
	serviceName     string
	namespace       string
	selfName        string
	peers           []string
	staticPeers     []string
	refreshInterval time.Duration
	mu              sync.RWMutex
	onPeersChanged  func(peers []string)
}

// Config holds the configuration for PeerDiscovery
type Config struct {
	// ServiceName is the name of the headless service (e.g., "kube-janitor")
	ServiceName string
	// Namespace is the Kubernetes namespace where the service exists
	Namespace string
	// SelfName is the identifier of this instance (typically HOSTNAME)
	SelfName string
	// RefreshInterval is how often to refresh the peer list
	RefreshInterval time.Duration
	// OnPeersChanged is called when the peer list changes
	OnPeersChanged func(peers []string)
	// StaticPeers is an optional list of peer names to use instead of DNS discovery
	// If set, DNS discovery is bypassed and these peers are used directly
	StaticPeers []string
}

// NewPeerDiscovery creates a new PeerDiscovery instance
func NewPeerDiscovery(config Config) *PeerDiscovery {
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = DefaultRefreshInterval
	}

	// Get self name from HOSTNAME if not provided
	if config.SelfName == "" {
		config.SelfName = os.Getenv("HOSTNAME")
	}

	// Get namespace from environment if not provided
	if config.Namespace == "" {
		config.Namespace = os.Getenv("POD_NAMESPACE")
		if config.Namespace == "" {
			// Try reading from the serviceaccount namespace file
			if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
				// BUG FIX: Trim whitespace/newlines that may be present in the file
				config.Namespace = strings.TrimSpace(string(data))
			}
		}
	}

	return &PeerDiscovery{
		serviceName:     config.ServiceName,
		namespace:       config.Namespace,
		selfName:        config.SelfName,
		peers:           []string{},
		staticPeers:     config.StaticPeers,
		refreshInterval: config.RefreshInterval,
		onPeersChanged:  config.OnPeersChanged,
	}
}

// Start begins the peer discovery background refresh loop
func (p *PeerDiscovery) Start(ctx context.Context) {
	// Initial refresh
	if err := p.RefreshPeers(); err != nil {
		logrus.WithError(err).Warn("Initial peer discovery failed, will retry on next interval")
	}

	// Start background refresh
	go p.refreshLoop(ctx)
}

// refreshLoop periodically refreshes the peer list
func (p *PeerDiscovery) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(p.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := p.RefreshPeers(); err != nil {
				logrus.WithError(err).Warn("Failed to refresh peers")
			}
		case <-ctx.Done():
			logrus.Debug("Peer discovery refresh loop stopped")
			return
		}
	}
}

// RefreshPeers queries DNS for the headless service and updates the peer list.
// If static peers are configured, uses those instead of DNS discovery.
func (p *PeerDiscovery) RefreshPeers() error {
	var peers []string

	// Use static peers if configured
	if len(p.staticPeers) > 0 {
		peers = make([]string, len(p.staticPeers))
		copy(peers, p.staticPeers)

		logrus.WithFields(logrus.Fields{
			"staticPeers": p.staticPeers,
			"count":       len(peers),
		}).Debug("Using static peers configuration")
	} else {
		// Build the full service DNS name
		dnsName := p.buildDNSName()

		logrus.WithFields(logrus.Fields{
			"dnsName":   dnsName,
			"namespace": p.namespace,
		}).Debug("Resolving headless service DNS")

		// Create a custom resolver with timeout
		resolver := &net.Resolver{
			PreferGo: true,
		}

		// BUG FIX: Create a fresh context for the LookupHost call
		// Each DNS operation gets its own timeout to prevent timeout budget exhaustion
		lookupCtx, lookupCancel := context.WithTimeout(context.Background(), DefaultDNSTimeout)
		defer lookupCancel()

		// Look up all IP addresses for the headless service
		addrs, err := resolver.LookupHost(lookupCtx, dnsName)
		if err != nil {
			return err
		}

		// Convert IPs to pod names by doing reverse lookups
		// BUG FIX: Pass resolver instead of context to allow fresh contexts per lookup
		peers = p.resolvePodsFromIPs(resolver, addrs)
	}

	// Sort for consistent ordering
	sort.Strings(peers)

	// Check if peers changed
	p.mu.Lock()
	oldPeers := p.peers
	changed := !equalStringSlices(oldPeers, peers)
	p.peers = peers
	callback := p.onPeersChanged
	p.mu.Unlock()

	if changed {
		logrus.WithFields(logrus.Fields{
			"oldPeers": oldPeers,
			"newPeers": peers,
			"count":    len(peers),
		}).Info("Peer list updated")

		if callback != nil {
			callback(peers)
		}
	}

	return nil
}

// IsUsingStaticPeers returns true if static peers are configured
func (p *PeerDiscovery) IsUsingStaticPeers() bool {
	return len(p.staticPeers) > 0
}

// resolvePodsFromIPs tries to resolve pod names from IP addresses
// For StatefulSets, pods have predictable DNS names like:
// <pod-name>.<service-name>.<namespace>.svc.cluster.local
// BUG FIX: Each reverse lookup gets its own fresh context with full timeout
func (p *PeerDiscovery) resolvePodsFromIPs(resolver *net.Resolver, ips []string) []string {
	peers := make([]string, 0, len(ips))

	for _, ip := range ips {
		// BUG FIX: Create a fresh context for each reverse DNS lookup
		// This ensures each lookup gets the full timeout budget
		lookupCtx, cancel := context.WithTimeout(context.Background(), DefaultDNSTimeout)

		// Try reverse DNS lookup
		names, err := resolver.LookupAddr(lookupCtx, ip)
		cancel() // Clean up context immediately after use

		if err == nil && len(names) > 0 {
			// Extract pod name from full DNS name
			// Format: <pod-name>.<service-name>.<namespace>.svc.cluster.local.
			podName := extractPodName(names[0])
			if podName != "" {
				peers = append(peers, podName)
				continue
			}
		}

		// Fallback: use IP as identifier (not ideal but works)
		logrus.WithFields(logrus.Fields{
			"ip":    ip,
			"error": err,
		}).Debug("Could not resolve pod name from IP, using IP as identifier")
		peers = append(peers, ip)
	}

	return peers
}

// buildDNSName constructs the full DNS name for the headless service
func (p *PeerDiscovery) buildDNSName() string {
	if p.namespace != "" {
		return p.serviceName + "." + p.namespace + ".svc.cluster.local"
	}
	return p.serviceName
}

// GetPeers returns the current list of known peers
func (p *PeerDiscovery) GetPeers() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	peers := make([]string, len(p.peers))
	copy(peers, p.peers)
	return peers
}

// GetSelfName returns the name of this instance
func (p *PeerDiscovery) GetSelfName() string {
	return p.selfName
}

// GetPeerCount returns the number of known peers
func (p *PeerDiscovery) GetPeerCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.peers)
}

// extractPodName extracts the pod name from a full DNS name
// Input: "kube-janitor-0.kube-janitor.default.svc.cluster.local."
// Output: "kube-janitor-0"
func extractPodName(dnsName string) string {
	if len(dnsName) == 0 {
		return ""
	}

	// Remove trailing dot if present
	if dnsName[len(dnsName)-1] == '.' {
		dnsName = dnsName[:len(dnsName)-1]
	}

	// Split by dots and take the first part (pod name)
	for i := 0; i < len(dnsName); i++ {
		if dnsName[i] == '.' {
			return dnsName[:i]
		}
	}

	return dnsName
}

// equalStringSlices checks if two string slices are equal
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
