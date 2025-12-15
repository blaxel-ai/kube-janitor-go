package janitor

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blaxel-ai/kube-janitor-go/internal/discovery"
	"github.com/blaxel-ai/kube-janitor-go/internal/metrics"
	"github.com/blaxel-ai/kube-janitor-go/internal/rules"
	"github.com/blaxel-ai/kube-janitor-go/internal/sharding"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

const (
	// Legacy annotations (for backward compatibility)
	annotationTTL     = "janitor/ttl"
	annotationExpires = "janitor/expires"

	// New policy-based annotations
	annotationDeleteIfMaxAge  = "janitor/delete-if-max-age"
	annotationDeleteIfIdle    = "janitor/delete-if-idle"
	annotationDeleteIfDate    = "janitor/delete-if-date"
	annotationArchiveIfMaxAge = "janitor/archive-if-max-age" // Future implementation
	annotationArchiveIfIdle   = "janitor/archive-if-idle"    // Future implementation
	annotationArchiveIfDate   = "janitor/archive-if-date"    // Future implementation

	// Timestamp annotations
	annotationLastUsedAt = "lastUsedAt"
)

// Action constants
const (
	ActionDelete  = "delete"
	ActionArchive = "archive" // Future implementation
)

// Config holds the janitor configuration
type Config struct {
	DryRun            bool
	Interval          time.Duration // Deprecated: use ReconcileInterval instead
	ReconcileInterval time.Duration // Interval for full reconciliation (default 10m)
	CheckInterval     time.Duration // Interval for expiration checks (default 1s)
	Once              bool
	IncludeResources  []string
	ExcludeResources  []string
	IncludeNamespaces []string
	ExcludeNamespaces []string
	RulesFile         string
	MaxWorkers        int
	DeletionDelay     time.Duration
	// Sharding configuration
	ShardingEnabled         bool
	ShardingServiceName     string
	ShardingNamespace       string
	ShardingRefreshInterval time.Duration
	ShardingStaticPeers     []string
}

// Janitor is the main cleanup controller
type Janitor struct {
	Clientset       kubernetes.Interface
	DynamicClient   dynamic.Interface
	DiscoveryClient k8sdiscovery.DiscoveryInterface
	Config          Config
	RuleEngine      *rules.Engine
	ResourceFilter  *ResourceFilter
	WorkQueue       chan WorkItem
	wg              sync.WaitGroup
	EventRecorder   record.EventRecorder
	// Sharding components
	HashRing      *sharding.HashRing
	PeerDiscovery *discovery.PeerDiscovery
	// Pending deletions tracking to avoid re-processing
	pendingDeletions sync.Map // map[string]time.Time - key is "namespace/name/resource"
	// Scheduled deletions - timers for future deletions
	scheduledDeletions sync.Map // map[string]*time.Timer - key is "namespace/name/resource"
	// Informer-based components
	expirationStore   *ExpirationStore
	informerFactory   dynamicinformer.DynamicSharedInformerFactory
	informerStopCh    chan struct{}
	watchedResources  []schema.GroupVersionResource // Resources being watched by informers
	watchedResourceMu sync.RWMutex
}

// WorkItem represents an item to be processed
type WorkItem struct {
	Resource  schema.GroupVersionResource
	Namespace string
	Name      string
	Obj       *unstructured.Unstructured
	// EnqueuedAt is when this item was put in the WorkQueue.
	// Used to make deletion delay dynamic and account for queue/loop latency.
	EnqueuedAt time.Time
}

// pendingDeletionKey generates a unique key for tracking pending deletions
func pendingDeletionKey(resource, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", resource, namespace, name)
}

// Pending deletion expiry time - after this duration, allow re-processing
const pendingDeletionExpiry = 2 * time.Minute

// New creates a new Janitor instance
func New(clientset kubernetes.Interface, restConfig *rest.Config, config Config) (*Janitor, error) {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := k8sdiscovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	var ruleEngine *rules.Engine
	if config.RulesFile != "" {
		ruleEngine, err = rules.LoadFromFile(config.RulesFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load rules: %w", err)
		}
	}

	resourceFilter := NewResourceFilter(config.IncludeResources, config.ExcludeResources,
		config.IncludeNamespaces, config.ExcludeNamespaces)

	// Create event broadcaster and recorder
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: clientset.CoreV1().Events("")})
	eventBroadcaster.StartStructuredLogging(0)
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
		Component: "kube-janitor",
		Host:      os.Getenv("HOSTNAME"),
	})

	// Set default intervals if not provided
	if config.ReconcileInterval == 0 {
		if config.Interval > 0 {
			// Use legacy Interval as fallback
			config.ReconcileInterval = config.Interval
		} else {
			config.ReconcileInterval = 10 * time.Minute
		}
	}
	if config.CheckInterval == 0 {
		config.CheckInterval = 1 * time.Second
	}

	// Create informer factory with resync period matching reconcile interval
	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, config.ReconcileInterval)

	j := &Janitor{
		Clientset:        clientset,
		DynamicClient:    dynamicClient,
		DiscoveryClient:  discoveryClient,
		Config:           config,
		RuleEngine:       ruleEngine,
		ResourceFilter:   resourceFilter,
		WorkQueue:        make(chan WorkItem, 1000),
		wg:               sync.WaitGroup{},
		EventRecorder:    recorder,
		expirationStore:  NewExpirationStore(),
		informerFactory:  informerFactory,
		informerStopCh:   make(chan struct{}),
		watchedResources: make([]schema.GroupVersionResource, 0),
	}

	// Initialize sharding if enabled
	if config.ShardingEnabled {
		selfName := os.Getenv("HOSTNAME")
		if selfName == "" {
			return nil, fmt.Errorf("HOSTNAME environment variable is required for sharding")
		}

		j.HashRing = sharding.NewHashRing(selfName, sharding.DefaultVirtualNodes)

		// Create peer discovery
		j.PeerDiscovery = discovery.NewPeerDiscovery(discovery.Config{
			ServiceName:     config.ShardingServiceName,
			Namespace:       config.ShardingNamespace,
			SelfName:        selfName,
			RefreshInterval: config.ShardingRefreshInterval,
			StaticPeers:     config.ShardingStaticPeers,
			OnPeersChanged: func(peers []string) {
				j.HashRing.SetNodes(peers)
				metrics.ShardingPeers.Set(float64(len(peers)))
				logrus.WithFields(logrus.Fields{
					"peers":    peers,
					"selfName": selfName,
				}).Info("Updated hash ring with new peers")
			},
		})

		logrus.WithFields(logrus.Fields{
			"selfName":        selfName,
			"serviceName":     config.ShardingServiceName,
			"namespace":       config.ShardingNamespace,
			"refreshInterval": config.ShardingRefreshInterval,
			"staticPeers":     config.ShardingStaticPeers,
		}).Info("Sharding enabled")
	}

	return j, nil
}

// Run starts the janitor
func (j *Janitor) Run(ctx context.Context) error {
	logrus.Info("Starting janitor")

	// Start peer discovery if sharding is enabled
	if j.Config.ShardingEnabled && j.PeerDiscovery != nil {
		j.PeerDiscovery.Start(ctx)
		logrus.Info("Peer discovery started for sharding")
	}

	// Start workers
	for i := 0; i < j.Config.MaxWorkers; i++ {
		j.wg.Add(1)
		go j.worker(ctx)
	}

	// Run cleanup loop
	if j.Config.Once {
		// In "once" mode, just do a full reconciliation and exit
		if err := j.reconcile(ctx); err != nil {
			metrics.Errors.WithLabelValues("cleanup").Inc()
			return err
		}
	} else {
		// Set up informers for all allowed resource types
		if err := j.setupInformers(ctx); err != nil {
			logrus.WithError(err).Error("Failed to setup informers")
			metrics.Errors.WithLabelValues("setup_informers").Inc()
			return err
		}

		// Start informers
		j.informerFactory.Start(j.informerStopCh)

		// Wait for informer caches to sync
		logrus.Info("Waiting for informer caches to sync...")
		j.informerFactory.WaitForCacheSync(j.informerStopCh)
		logrus.Info("Informer caches synced")

		// Start the expiration check loop (1 second)
		j.wg.Add(1)
		go j.runExpirationCheckLoop(ctx)

		// Start the reconciliation loop (10 minutes)
		j.wg.Add(1)
		go j.runReconcileLoop(ctx)

		// Wait for shutdown
		<-ctx.Done()
		logrus.Info("Shutting down janitor")

		// Stop informers
		close(j.informerStopCh)

		// Cancel all scheduled deletions
		j.cancelAllScheduledDeletions()

		close(j.WorkQueue)
		j.wg.Wait()
		return nil
	}

	close(j.WorkQueue)
	j.wg.Wait()
	return nil
}

// setupInformers discovers all allowed resource types and sets up informers for them
func (j *Janitor) setupInformers(ctx context.Context) error {
	logrus.Debug("Setting up informers for allowed resource types")

	// Get all resource types
	resources, err := j.DiscoveryClient.ServerPreferredResources()
	if err != nil {
		return fmt.Errorf("failed to discover resources: %w", err)
	}

	var watchedGVRs []schema.GroupVersionResource

	for _, resourceList := range resources {
		if resourceList == nil {
			continue
		}

		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to parse group version %s", resourceList.GroupVersion)
			continue
		}

		for _, resource := range resourceList.APIResources {
			// Skip resources that can't be listed, watched, or deleted
			if !contains(resource.Verbs, "list") || !contains(resource.Verbs, "watch") || !contains(resource.Verbs, "delete") {
				continue
			}

			// Apply resource filter
			if !j.ResourceFilter.ShouldProcessResource(resource.Name) {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: resource.Name,
			}

			// Set up informer for this resource type
			informer := j.informerFactory.ForResource(gvr)
			_, err := informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc:    j.createAddHandler(gvr),
				UpdateFunc: j.createUpdateHandler(gvr),
				DeleteFunc: j.createDeleteHandler(gvr),
			})
			if err != nil {
				logrus.WithError(err).WithField("resource", gvr.Resource).Warn("Failed to add event handler")
				continue
			}

			watchedGVRs = append(watchedGVRs, gvr)
			logrus.WithField("resource", gvr.Resource).Debug("Set up informer")
		}
	}

	j.watchedResourceMu.Lock()
	j.watchedResources = watchedGVRs
	j.watchedResourceMu.Unlock()

	logrus.WithField("count", len(watchedGVRs)).Info("Informers set up for resource types")
	return nil
}

// createAddHandler creates an event handler for Add events
func (j *Janitor) createAddHandler(gvr schema.GroupVersionResource) func(obj interface{}) {
	return func(obj interface{}) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}

		// Check namespace filter
		if u.GetNamespace() != "" && !j.ResourceFilter.ShouldProcessNamespace(u.GetNamespace()) {
			return
		}

		// Check sharding
		if j.Config.ShardingEnabled && j.HashRing != nil {
			if !j.HashRing.ShouldProcess(u.GetNamespace(), u.GetName()) {
				return
			}
		}

		// Check if resource has expiration
		expirationTime, reason := j.getExpirationTime(u)
		if expirationTime.IsZero() {
			return
		}

		// Add to store
		j.expirationStore.Add(gvr, u.GetNamespace(), u.GetName(), expirationTime, reason)

		logrus.WithFields(logrus.Fields{
			"resource":   gvr.Resource,
			"namespace":  u.GetNamespace(),
			"name":       u.GetName(),
			"expiration": expirationTime,
			"reason":     reason,
		}).Debug("Added resource to expiration store")
	}
}

// createUpdateHandler creates an event handler for Update events
func (j *Janitor) createUpdateHandler(gvr schema.GroupVersionResource) func(oldObj, newObj interface{}) {
	return func(oldObj, newObj interface{}) {
		u, ok := newObj.(*unstructured.Unstructured)
		if !ok {
			return
		}

		// Check namespace filter
		if u.GetNamespace() != "" && !j.ResourceFilter.ShouldProcessNamespace(u.GetNamespace()) {
			// Remove from store if it was there
			j.expirationStore.Remove(gvr, u.GetNamespace(), u.GetName())
			return
		}

		// Check sharding
		if j.Config.ShardingEnabled && j.HashRing != nil {
			if !j.HashRing.ShouldProcess(u.GetNamespace(), u.GetName()) {
				// Remove from store if sharding changed
				j.expirationStore.Remove(gvr, u.GetNamespace(), u.GetName())
				return
			}
		}

		// Check if resource has expiration
		expirationTime, reason := j.getExpirationTime(u)
		if expirationTime.IsZero() {
			// Remove from store if TTL annotation was removed
			j.expirationStore.Remove(gvr, u.GetNamespace(), u.GetName())
			logrus.WithFields(logrus.Fields{
				"resource":  gvr.Resource,
				"namespace": u.GetNamespace(),
				"name":      u.GetName(),
			}).Debug("Removed resource from expiration store (no TTL)")
			return
		}

		// Add/update in store
		j.expirationStore.Add(gvr, u.GetNamespace(), u.GetName(), expirationTime, reason)

		logrus.WithFields(logrus.Fields{
			"resource":   gvr.Resource,
			"namespace":  u.GetNamespace(),
			"name":       u.GetName(),
			"expiration": expirationTime,
			"reason":     reason,
		}).Debug("Updated resource in expiration store")
	}
}

// createDeleteHandler creates an event handler for Delete events
func (j *Janitor) createDeleteHandler(gvr schema.GroupVersionResource) func(obj interface{}) {
	return func(obj interface{}) {
		var u *unstructured.Unstructured
		var ok bool

		// Handle DeletedFinalStateUnknown
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			u, ok = tombstone.Obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
		} else {
			u, ok = obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
		}

		// Remove from store
		j.expirationStore.Remove(gvr, u.GetNamespace(), u.GetName())

		// Also clean up pending deletions
		key := pendingDeletionKey(gvr.Resource, u.GetNamespace(), u.GetName())
		j.pendingDeletions.Delete(key)
		if timer, exists := j.scheduledDeletions.LoadAndDelete(key); exists {
			if t, ok := timer.(*time.Timer); ok {
				t.Stop()
			}
		}

		logrus.WithFields(logrus.Fields{
			"resource":  gvr.Resource,
			"namespace": u.GetNamespace(),
			"name":      u.GetName(),
		}).Debug("Removed resource from expiration store (deleted)")
	}
}

// runExpirationCheckLoop runs the expiration check loop every CheckInterval
func (j *Janitor) runExpirationCheckLoop(ctx context.Context) {
	defer j.wg.Done()

	ticker := time.NewTicker(j.Config.CheckInterval)
	defer ticker.Stop()

	logrus.WithField("interval", j.Config.CheckInterval).Info("Starting expiration check loop")

	for {
		select {
		case <-ticker.C:
			j.checkExpirations()
		case <-ctx.Done():
			logrus.Info("Expiration check loop stopped")
			return
		}
	}
}

// checkExpirations checks the store for expired resources and queues them for deletion
func (j *Janitor) checkExpirations() {
	expired := j.expirationStore.GetExpired()
	if len(expired) == 0 {
		return
	}

	logrus.WithField("count", len(expired)).Debug("Found expired resources")

	for _, entry := range expired {
		key := pendingDeletionKey(entry.GVR.Resource, entry.Namespace, entry.Name)

		// Skip if already pending deletion
		if timestamp, exists := j.pendingDeletions.Load(key); exists {
			if time.Since(timestamp.(time.Time)) < pendingDeletionExpiry {
				continue
			}
			j.pendingDeletions.Delete(key)
		}

		// Skip if already scheduled
		if _, exists := j.scheduledDeletions.Load(key); exists {
			continue
		}

		// Re-check sharding ownership
		if j.Config.ShardingEnabled && j.HashRing != nil {
			if !j.HashRing.ShouldProcess(entry.Namespace, entry.Name) {
				// Remove from store - not our responsibility
				j.expirationStore.Remove(entry.GVR, entry.Namespace, entry.Name)
				continue
			}
		}

		// Mark as pending and queue for deletion
		j.pendingDeletions.Store(key, time.Now())

		// Remove from store since we're processing it
		j.expirationStore.Remove(entry.GVR, entry.Namespace, entry.Name)

		// Queue for deletion
		select {
		case j.WorkQueue <- WorkItem{
			Resource:   entry.GVR,
			Namespace:  entry.Namespace,
			Name:       entry.Name,
			Obj:        nil, // Will be fetched fresh in processItem
			EnqueuedAt: time.Now(),
		}:
			logrus.WithFields(logrus.Fields{
				"resource":  entry.GVR.Resource,
				"namespace": entry.Namespace,
				"name":      entry.Name,
				"reason":    entry.Reason,
			}).Debug("Queued expired resource for deletion")
		default:
			// Queue is full, will be retried on next check
			j.pendingDeletions.Delete(key)
			logrus.WithFields(logrus.Fields{
				"resource":  entry.GVR.Resource,
				"namespace": entry.Namespace,
				"name":      entry.Name,
			}).Warn("Work queue full, will retry")
		}
	}
}

// runReconcileLoop runs the full reconciliation loop every ReconcileInterval
func (j *Janitor) runReconcileLoop(ctx context.Context) {
	defer j.wg.Done()

	ticker := time.NewTicker(j.Config.ReconcileInterval)
	defer ticker.Stop()

	logrus.WithField("interval", j.Config.ReconcileInterval).Info("Starting reconciliation loop")

	// Run immediately on startup
	if err := j.reconcile(ctx); err != nil {
		logrus.WithError(err).Error("Reconciliation failed")
		metrics.Errors.WithLabelValues("reconcile").Inc()
	}

	for {
		select {
		case <-ticker.C:
			if err := j.reconcile(ctx); err != nil {
				logrus.WithError(err).Error("Reconciliation failed")
				metrics.Errors.WithLabelValues("reconcile").Inc()
			}
		case <-ctx.Done():
			logrus.Info("Reconciliation loop stopped")
			return
		}
	}
}

// reconcile performs a full reconciliation of the expiration store with the cluster state
func (j *Janitor) reconcile(ctx context.Context) error {
	logrus.Debug("Starting reconciliation")
	timer := prometheus.NewTimer(metrics.CleanupDuration)
	defer timer.ObserveDuration()

	j.watchedResourceMu.RLock()
	gvrs := j.watchedResources
	j.watchedResourceMu.RUnlock()

	// If informers haven't been set up yet (e.g., in "once" mode), discover resources
	if len(gvrs) == 0 {
		resources, err := j.DiscoveryClient.ServerPreferredResources()
		if err != nil {
			return fmt.Errorf("failed to discover resources: %w", err)
		}

		for _, resourceList := range resources {
			if resourceList == nil {
				continue
			}

			gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
			if err != nil {
				continue
			}

			for _, resource := range resourceList.APIResources {
				if !contains(resource.Verbs, "list") || !contains(resource.Verbs, "delete") {
					continue
				}
				if !j.ResourceFilter.ShouldProcessResource(resource.Name) {
					continue
				}
				gvrs = append(gvrs, schema.GroupVersionResource{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: resource.Name,
				})
			}
		}
	}

	// Get namespaces
	namespaces, err := j.getNamespaces(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to list namespaces")
		metrics.Errors.WithLabelValues("list_namespaces").Inc()
		namespaces = []string{}
	}

	// Filter namespaces
	var filteredNamespaces []string
	for _, ns := range namespaces {
		if j.ResourceFilter.ShouldProcessNamespace(ns) {
			filteredNamespaces = append(filteredNamespaces, ns)
		}
	}

	// Process each resource type
	var wg sync.WaitGroup
	sem := make(chan struct{}, j.Config.MaxWorkers)

	for _, gvr := range gvrs {
		for _, ns := range filteredNamespaces {
			wg.Add(1)
			sem <- struct{}{}
			go func(gvr schema.GroupVersionResource, namespace string) {
				defer wg.Done()
				defer func() { <-sem }()
				j.reconcileResourceNamespace(ctx, gvr, namespace)
			}(gvr, ns)
		}
		// Also handle cluster-scoped resources
		wg.Add(1)
		sem <- struct{}{}
		go func(gvr schema.GroupVersionResource) {
			defer wg.Done()
			defer func() { <-sem }()
			j.reconcileResourceNamespace(ctx, gvr, "")
		}(gvr)
	}

	wg.Wait()
	logrus.Info("Reconciliation completed")
	return nil
}

// reconcileResourceNamespace reconciles a single resource type in a namespace
func (j *Janitor) reconcileResourceNamespace(ctx context.Context, gvr schema.GroupVersionResource, namespace string) {
	var resourceInterface dynamic.ResourceInterface
	if namespace != "" {
		resourceInterface = j.DynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resourceInterface = j.DynamicClient.Resource(gvr)
	}

	list, err := resourceInterface.List(ctx, metav1.ListOptions{})
	if err != nil {
		// Silently skip - might be a cluster-scoped resource in a namespace context
		return
	}

	for _, item := range list.Items {
		obj := item
		objNamespace := obj.GetNamespace()
		objName := obj.GetName()

		// Skip resources that are already being deleted
		if obj.GetDeletionTimestamp() != nil {
			continue
		}

		// Check namespace filter
		if objNamespace != "" && !j.ResourceFilter.ShouldProcessNamespace(objNamespace) {
			continue
		}

		// Check sharding
		if j.Config.ShardingEnabled && j.HashRing != nil {
			if !j.HashRing.ShouldProcess(objNamespace, objName) {
				continue
			}
		}

		// Track evaluated resources
		metrics.ResourcesEvaluated.WithLabelValues(gvr.Resource, namespace).Inc()

		// Get expiration time
		expirationTime, reason := j.getExpirationTime(&obj)
		if expirationTime.IsZero() {
			// No expiration - make sure it's not in the store
			j.expirationStore.Remove(gvr, objNamespace, objName)
			continue
		}

		// Update store with current state
		j.expirationStore.Add(gvr, objNamespace, objName, expirationTime, reason)
	}
}

// scheduleDeletion schedules a resource for deletion at a specific time
// Only stores minimal info (name, namespace, gvr) - fetches fresh data when timer fires
func (j *Janitor) scheduleDeletion(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, expirationTime time.Time, reason string) {
	key := pendingDeletionKey(gvr.Resource, namespace, name)
	delay := time.Until(expirationTime)

	// Don't schedule if delay is negative or very small
	if delay < 100*time.Millisecond {
		// Process immediately instead - will fetch fresh data in processItem
		j.WorkQueue <- WorkItem{
			Resource:   gvr,
			Namespace:  namespace,
			Name:       name,
			Obj:        nil, // Will be fetched in processItem
			EnqueuedAt: time.Now(),
		}
		return
	}

	logger := logrus.WithFields(logrus.Fields{
		"resource":   gvr.Resource,
		"namespace":  namespace,
		"name":       name,
		"delay":      delay,
		"expiration": expirationTime,
		"reason":     reason,
	})

	timer := time.AfterFunc(delay, func() {
		// Remove from scheduled deletions
		j.scheduledDeletions.Delete(key)

		// Re-check sharding ownership before queuing
		// The hash ring may have changed since the deletion was scheduled
		if j.Config.ShardingEnabled && j.HashRing != nil {
			if !j.HashRing.ShouldProcess(namespace, name) {
				logger.Debug("Scheduled deletion skipped (no longer owned by this instance)")
				return
			}
		}

		// Queue for deletion - Obj is nil, will be fetched fresh in processItem
		select {
		case j.WorkQueue <- WorkItem{
			Resource:   gvr,
			Namespace:  namespace,
			Name:       name,
			Obj:        nil,
			EnqueuedAt: time.Now(),
		}:
		case <-ctx.Done():
			logger.Debug("Context cancelled, skipping scheduled deletion")
		}
	})

	// Store the timer
	j.scheduledDeletions.Store(key, timer)
	logger.Debug("Scheduled future deletion")
}

// cancelAllScheduledDeletions cancels all pending scheduled deletions
func (j *Janitor) cancelAllScheduledDeletions() {
	count := 0
	j.scheduledDeletions.Range(func(key, value interface{}) bool {
		if timer, ok := value.(*time.Timer); ok {
			timer.Stop()
		}
		j.scheduledDeletions.Delete(key)
		count++
		return true
	})
	if count > 0 {
		logrus.WithField("count", count).Info("Cancelled scheduled deletions")
	}
}

func (j *Janitor) worker(ctx context.Context) {
	defer j.wg.Done()

	for {
		select {
		case item, ok := <-j.WorkQueue:
			if !ok {
				return
			}
			j.processItem(ctx, item)
		case <-ctx.Done():
			return
		}
	}
}

func (j *Janitor) processItem(ctx context.Context, item WorkItem) {
	logger := logrus.WithFields(logrus.Fields{
		"resource":  item.Resource.Resource,
		"namespace": item.Namespace,
		"name":      item.Name,
	})

	// Re-check sharding ownership before processing
	// The hash ring may have changed since the item was scheduled
	if j.Config.ShardingEnabled && j.HashRing != nil {
		if !j.HashRing.ShouldProcess(item.Namespace, item.Name) {
			logger.Debug("Skipping resource (no longer owned by this instance after hash ring change)")
			metrics.ResourcesSkipped.WithLabelValues(item.Resource.Resource, item.Namespace).Inc()
			return
		}
	}

	key := pendingDeletionKey(item.Resource.Resource, item.Namespace, item.Name)

	// Get resource interface
	var resourceInterface dynamic.ResourceInterface
	if item.Namespace != "" {
		resourceInterface = j.DynamicClient.Resource(item.Resource).Namespace(item.Namespace)
	} else {
		resourceInterface = j.DynamicClient.Resource(item.Resource)
	}

	// If Obj is nil (scheduled deletion), fetch it fresh
	obj := item.Obj
	if obj == nil {
		var err error
		obj, err = resourceInterface.Get(ctx, item.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Debug("Resource no longer exists")
				return
			}
			logger.WithError(err).Error("Failed to fetch resource")
			metrics.Errors.WithLabelValues("fetch_resource").Inc()
			return
		}
	}

	queuedAt := item.EnqueuedAt
	if queuedAt.IsZero() {
		queuedAt = time.Now()
	}

	// Check if resource should be deleted
	shouldDelete, reason := j.shouldDelete(obj)
	if !shouldDelete {
		return
	}

	// Use a stable reason for events when possible (avoid embedding volatile data like "age")
	// This helps Kubernetes event de-duplication and reduces noisy repeated Events.
	eventReason := reason
	if expirationTime, expirationReason := j.getExpirationTime(obj); !expirationTime.IsZero() && expirationReason != "" {
		eventReason = expirationReason
	}

	// Mark as pending deletion to prevent re-queuing
	j.pendingDeletions.Store(key, time.Now())

	// Create a reference to the object for the event
	ref := &corev1.ObjectReference{
		APIVersion: item.Resource.Group + "/" + item.Resource.Version,
		Kind:       obj.GetKind(),
		Namespace:  item.Namespace,
		Name:       item.Name,
		UID:        obj.GetUID(),
	}

	if j.Config.DryRun {
		logger.Info("DRY RUN: Would delete resource")
		// Create event for dry-run
		eventMessage := fmt.Sprintf("DRY RUN: Would delete %s %s/%s - %s",
			item.Resource.Resource, item.Namespace, item.Name, eventReason)
		j.EventRecorder.Event(ref, corev1.EventTypeNormal, "DryRunDeletion", eventMessage)
		// Remove from pending since we're not actually deleting
		j.pendingDeletions.Delete(key)
		return
	}

	// Store the original UID to detect resource recreation
	originalUID := obj.GetUID()

	// If deletion delay is configured, send event first and wait
	if j.Config.DeletionDelay > 0 {
		// Make the delay dynamic: subtract time already spent since this item was enqueued.
		// Clamp to [0, Config.DeletionDelay] so we never wait more than configured.
		elapsed := time.Since(queuedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		remainingDelay := j.Config.DeletionDelay - elapsed
		if remainingDelay < 0 {
			remainingDelay = 0
		}
		if remainingDelay > j.Config.DeletionDelay {
			remainingDelay = j.Config.DeletionDelay
		}

		// Send scheduled deletion event immediately
		eventMessage := fmt.Sprintf("Scheduled for deletion in %s: %s %s/%s - %s",
			remainingDelay, item.Resource.Resource, item.Namespace, item.Name, eventReason)
		j.EventRecorder.Event(ref, corev1.EventTypeNormal, "ResourceScheduledForDeletion", eventMessage)

		if remainingDelay > 0 {
			// Wait for the remaining delay, respecting context cancellation
			select {
			case <-time.After(remainingDelay):
				// Continue with deletion
			case <-ctx.Done():
				logger.Info("Context cancelled during deletion delay")
				j.pendingDeletions.Delete(key)
				return
			}
		}

		// Re-fetch the resource to check if it still exists and still needs deletion
		freshObj, err := resourceInterface.Get(ctx, item.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Resource no longer exists, skipping deletion")
				j.pendingDeletions.Delete(key)
				return
			}
			logger.WithError(err).Error("Failed to re-fetch resource")
			metrics.Errors.WithLabelValues("refetch_resource").Inc()
			j.pendingDeletions.Delete(key)
			return
		}

		// Check if the resource was recreated (different UID means it's a new resource)
		if freshObj.GetUID() != originalUID {
			logger.WithFields(logrus.Fields{
				"originalUID": originalUID,
				"newUID":      freshObj.GetUID(),
			}).Info("Resource was recreated, cancelling scheduled deletion for old resource")
			// Remove from pending deletions so the new resource can be scheduled
			j.pendingDeletions.Delete(key)
			// Remove from scheduled deletions so the new resource can be evaluated
			j.scheduledDeletions.Delete(key)
			return
		}

		// Re-check if resource should still be deleted
		stillShouldDelete, newReason := j.shouldDelete(freshObj)
		if !stillShouldDelete {
			logger.Info("Resource no longer needs deletion after delay")
			// Send cancellation event
			cancelMessage := fmt.Sprintf("Deletion cancelled for %s %s/%s - conditions no longer met",
				item.Resource.Resource, item.Namespace, item.Name)
			j.EventRecorder.Event(ref, corev1.EventTypeNormal, "DeletionCancelled", cancelMessage)
			return
		}

		// Update reason if it changed
		if newReason != reason {
			reason = newReason
		}
		// Refresh stable event reason based on the latest object state
		eventReason = reason
		if expirationTime, expirationReason := j.getExpirationTime(freshObj); !expirationTime.IsZero() && expirationReason != "" {
			eventReason = expirationReason
		}
	}

	// Delete the resource
	err := resourceInterface.Delete(ctx, item.Name, metav1.DeleteOptions{})
	if err != nil {
		// Handle "not found" as success - the resource is already gone
		if apierrors.IsNotFound(err) {
			logger.Debug("Resource already deleted (not found)")
			j.pendingDeletions.Delete(key)
			return
		}
		logger.WithError(err).Error("Failed to delete resource")
		metrics.Errors.WithLabelValues("delete_resource").Inc()
		// Create event for failed deletion
		eventMessage := fmt.Sprintf("Failed to delete %s %s/%s: %v",
			item.Resource.Resource, item.Namespace, item.Name, err)
		j.EventRecorder.Event(ref, corev1.EventTypeWarning, "DeletionFailed", eventMessage)
		j.pendingDeletions.Delete(key)
		return
	}

	metrics.ResourcesDeleted.WithLabelValues(item.Resource.Resource, item.Namespace, reason).Inc()

	// Create event for successful deletion
	eventMessage := fmt.Sprintf("Deleted %s %s/%s - %s",
		item.Resource.Resource, item.Namespace, item.Name, eventReason)
	j.EventRecorder.Event(ref, corev1.EventTypeNormal, "ResourceDeleted", eventMessage)

	// Keep in pending deletions for a bit to avoid race conditions
	// It will be automatically cleaned up after pendingDeletionExpiry
}

// evaluateDeleteIfMaxAge checks if resource should be deleted based on max age
func (j *Janitor) evaluateDeleteIfMaxAge(value string, obj *unstructured.Unstructured) (bool, string, error) {
	duration, err := ParseExtendedDuration(value)
	if err != nil {
		return false, "", fmt.Errorf("invalid duration format: %w", err)
	}

	age := time.Since(obj.GetCreationTimestamp().Time)
	if age > duration {
		return true, fmt.Sprintf("Delete max-age policy triggered (age: %s, max-age: %s)", age, duration), nil
	}
	return false, "", nil
}

// evaluateDeleteIfDate checks if resource should be deleted based on expiration date
func (j *Janitor) evaluateDeleteIfDate(value string, obj *unstructured.Unstructured) (bool, string, error) {
	expirationTime, err := parseExpirationTime(value)
	if err != nil {
		return false, "", fmt.Errorf("invalid date format: %w", err)
	}

	now := time.Now()
	if now.After(expirationTime) {
		logrus.WithFields(logrus.Fields{
			"resource":       obj.GetKind(),
			"namespace":      obj.GetNamespace(),
			"name":           obj.GetName(),
			"expirationTime": expirationTime,
			"currentTime":    now,
			"expired":        true,
		}).Debug("Delete-if-date policy: resource expired")
		return true, fmt.Sprintf("Delete date policy triggered (expiration: %s)", value), nil
	}

	logrus.WithFields(logrus.Fields{
		"resource":        obj.GetKind(),
		"namespace":       obj.GetNamespace(),
		"name":            obj.GetName(),
		"expirationTime":  expirationTime,
		"currentTime":     now,
		"timeUntilExpiry": expirationTime.Sub(now),
		"expired":         false,
	}).Debug("Delete-if-date policy: resource not yet expired")
	return false, "", nil
}

// evaluateArchiveIfDate is a mock implementation for future archive-if-date functionality
func (j *Janitor) evaluateArchiveIfDate(value string, obj *unstructured.Unstructured) (bool, string, error) {
	// Mock implementation - archive functionality not yet implemented
	logrus.WithFields(logrus.Fields{
		"resource":  obj.GetKind(),
		"namespace": obj.GetNamespace(),
		"name":      obj.GetName(),
		"value":     value,
	}).Debug("Archive-if-date policy detected but archive functionality is not yet implemented")
	return false, "", nil
}

// evaluateDeleteIfIdle checks if resource should be deleted based on idle time
func (j *Janitor) evaluateDeleteIfIdle(value string, obj *unstructured.Unstructured) (bool, string, error) {
	logrus.WithFields(logrus.Fields{
		"resource":             obj.GetKind(),
		"namespace":            obj.GetNamespace(),
		"name":                 obj.GetName(),
		"value":                value,
		"annotationLastUsedAt": annotationLastUsedAt,
	}).Debug("Starting evaluateDeleteIfIdle")

	// Check if lastUsedAt annotation exists
	lastUsedAtStr, exists := obj.GetAnnotations()[annotationLastUsedAt]
	logrus.WithFields(logrus.Fields{
		"resource":      obj.GetKind(),
		"namespace":     obj.GetNamespace(),
		"name":          obj.GetName(),
		"lastUsedAtStr": lastUsedAtStr,
		"exists":        exists,
	}).Debug("Checking lastUsedAt annotation")

	if !exists {
		// If no lastUsedAt annotation, ignore this policy
		logrus.WithFields(logrus.Fields{
			"resource":  obj.GetKind(),
			"namespace": obj.GetNamespace(),
			"name":      obj.GetName(),
		}).Debug("Delete-if-idle policy ignored: no lastUsedAt annotation found")
		return false, "", nil
	}

	lastUsedAt, err := time.Parse(time.RFC3339, lastUsedAtStr)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"resource":      obj.GetKind(),
			"namespace":     obj.GetNamespace(),
			"name":          obj.GetName(),
			"lastUsedAtStr": lastUsedAtStr,
		}).Debug("Failed to parse lastUsedAt timestamp")
		return false, "", fmt.Errorf("invalid lastUsedAt format: %w", err)
	}

	duration, err := ParseExtendedDuration(value)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"resource":  obj.GetKind(),
			"namespace": obj.GetNamespace(),
			"name":      obj.GetName(),
			"value":     value,
		}).Debug("Failed to parse duration")
		return false, "", fmt.Errorf("invalid duration format: %w", err)
	}

	idleTime := time.Since(lastUsedAt)
	logrus.WithFields(logrus.Fields{
		"resource":     obj.GetKind(),
		"namespace":    obj.GetNamespace(),
		"name":         obj.GetName(),
		"idleTime":     idleTime,
		"duration":     duration,
		"shouldDelete": idleTime > duration,
		"lastUsedAt":   lastUsedAt,
	}).Debug("Evaluating idle time")

	if idleTime > duration {
		return true, fmt.Sprintf("Delete idle policy triggered (idle: %s, max-idle: %s)", idleTime, duration), nil
	}
	return false, "", nil
}

// findAnnotationsWithPrefix finds all annotations that start with the given prefix
func findAnnotationsWithPrefix(annotations map[string]string, prefix string) map[string]string {
	result := make(map[string]string)
	for key, value := range annotations {
		if strings.HasPrefix(key, prefix) {
			result[key] = value
		}
	}
	return result
}

// getExpirationTime returns when a resource will expire and the reason
// Returns zero time if no expiration is set
func (j *Janitor) getExpirationTime(obj *unstructured.Unstructured) (time.Time, string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return time.Time{}, ""
	}

	var earliestExpiration time.Time
	var reason string

	// Check legacy TTL annotation (most common case)
	if ttl, ok := annotations[annotationTTL]; ok {
		duration, err := ParseExtendedDuration(ttl)
		if err == nil {
			expiration := obj.GetCreationTimestamp().Time.Add(duration)
			if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
				earliestExpiration = expiration
				reason = fmt.Sprintf("Legacy TTL (ttl: %s)", ttl)
			}
		}
	}

	// Check delete-if-max-age annotations
	maxAgeAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfMaxAge)
	for annotationKey, annotationValue := range maxAgeAnnotations {
		duration, err := ParseExtendedDuration(annotationValue)
		if err == nil {
			expiration := obj.GetCreationTimestamp().Time.Add(duration)
			if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
				earliestExpiration = expiration
				reason = fmt.Sprintf("Delete max-age policy (annotation: %s)", annotationKey)
			}
		}
	}

	// Check delete-if-date annotations
	dateAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfDate)
	for annotationKey, annotationValue := range dateAnnotations {
		expiration, err := parseExpirationTime(annotationValue)
		if err == nil {
			if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
				earliestExpiration = expiration
				reason = fmt.Sprintf("Delete date policy (annotation: %s)", annotationKey)
			}
		}
	}

	// Check legacy expiration annotation
	if expires, ok := annotations[annotationExpires]; ok {
		expiration, err := parseExpirationTime(expires)
		if err == nil {
			if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
				earliestExpiration = expiration
				reason = fmt.Sprintf("Legacy expiration (%s)", expires)
			}
		}
	}

	// Check delete-if-idle annotations
	idleAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfIdle)
	if len(idleAnnotations) > 0 {
		// Get lastUsedAt timestamp
		if lastUsedAtStr, exists := annotations[annotationLastUsedAt]; exists {
			lastUsedAt, err := time.Parse(time.RFC3339, lastUsedAtStr)
			if err == nil {
				for annotationKey, annotationValue := range idleAnnotations {
					duration, err := ParseExtendedDuration(annotationValue)
					if err == nil {
						expiration := lastUsedAt.Add(duration)
						if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
							earliestExpiration = expiration
							reason = fmt.Sprintf("Delete idle policy (annotation: %s)", annotationKey)
						}
					}
				}
			}
		}
	}

	// Check rules
	if j.RuleEngine != nil {
		if rule, ttl := j.RuleEngine.Evaluate(obj); rule != nil {
			expiration := obj.GetCreationTimestamp().Time.Add(ttl)
			if earliestExpiration.IsZero() || expiration.Before(earliestExpiration) {
				earliestExpiration = expiration
				reason = fmt.Sprintf("Rule '%s' (ttl: %s)", rule.ID, ttl)
			}
		}
	}

	return earliestExpiration, reason
}

func (j *Janitor) shouldDelete(obj *unstructured.Unstructured) (bool, string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	logrus.WithFields(logrus.Fields{
		"resource":    obj.GetKind(),
		"namespace":   obj.GetNamespace(),
		"name":        obj.GetName(),
		"annotations": annotations,
	}).Debug("Evaluating resource for deletion")

	// Check all delete-if-max-age annotations (including numbered variants like janitor/delete-if-max-age-1, janitor/delete-if-max-age-2, etc.)
	maxAgeAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfMaxAge)
	logrus.WithFields(logrus.Fields{
		"resource":          obj.GetKind(),
		"namespace":         obj.GetNamespace(),
		"name":              obj.GetName(),
		"maxAgeAnnotations": len(maxAgeAnnotations),
	}).Debug("Checking delete-if-max-age annotations")
	for annotationKey, annotationValue := range maxAgeAnnotations {
		shouldDelete, reason, err := j.evaluateDeleteIfMaxAge(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-max-age policy")
			continue
		}
		if shouldDelete {
			return true, fmt.Sprintf("%s (annotation: %s)", reason, annotationKey)
		}
	}

	// Check all delete-if-idle annotations (including numbered variants like janitor/delete-if-idle-1, janitor/delete-if-idle-2, etc.)
	idleAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfIdle)
	logrus.WithFields(logrus.Fields{
		"resource":               obj.GetKind(),
		"namespace":              obj.GetNamespace(),
		"name":                   obj.GetName(),
		"idleAnnotations":        len(idleAnnotations),
		"annotationDeleteIfIdle": annotationDeleteIfIdle,
	}).Debug("Checking delete-if-idle annotations")
	for annotationKey, annotationValue := range idleAnnotations {
		logrus.WithFields(logrus.Fields{
			"resource":   obj.GetKind(),
			"namespace":  obj.GetNamespace(),
			"name":       obj.GetName(),
			"annotation": annotationKey,
			"value":      annotationValue,
		}).Debug("Processing delete-if-idle annotation")
		shouldDelete, reason, err := j.evaluateDeleteIfIdle(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-idle policy")
			continue
		}
		if shouldDelete {
			return true, fmt.Sprintf("%s (annotation: %s)", reason, annotationKey)
		}
	}

	// Check all delete-if-date annotations (including numbered variants like janitor/delete-if-date-1, janitor/delete-if-date-2, etc.)
	dateAnnotations := findAnnotationsWithPrefix(annotations, annotationDeleteIfDate)
	logrus.WithFields(logrus.Fields{
		"resource":        obj.GetKind(),
		"namespace":       obj.GetNamespace(),
		"name":            obj.GetName(),
		"dateAnnotations": len(dateAnnotations),
	}).Debug("Checking delete-if-date annotations")
	for annotationKey, annotationValue := range dateAnnotations {
		shouldDelete, reason, err := j.evaluateDeleteIfDate(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-date policy")
			continue
		}
		if shouldDelete {
			return true, fmt.Sprintf("%s (annotation: %s)", reason, annotationKey)
		}
	}

	// Check all archive-if-date annotations (including numbered variants) - mock implementation
	archiveDateAnnotations := findAnnotationsWithPrefix(annotations, annotationArchiveIfDate)
	logrus.WithFields(logrus.Fields{
		"resource":               obj.GetKind(),
		"namespace":              obj.GetNamespace(),
		"name":                   obj.GetName(),
		"archiveDateAnnotations": len(archiveDateAnnotations),
	}).Debug("Checking archive-if-date annotations")
	for annotationKey, annotationValue := range archiveDateAnnotations {
		shouldArchive, reason, err := j.evaluateArchiveIfDate(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate archive-if-date policy")
			continue
		}
		if shouldArchive {
			// Archive functionality not yet implemented, so we just log for now
			logrus.WithFields(logrus.Fields{
				"resource":   obj.GetKind(),
				"namespace":  obj.GetNamespace(),
				"name":       obj.GetName(),
				"annotation": annotationKey,
				"reason":     reason,
			}).Info("Archive-if-date policy would trigger (not yet implemented)")
		}
	}

	// Fallback to legacy TTL annotation for backward compatibility
	logrus.WithFields(logrus.Fields{
		"resource":  obj.GetKind(),
		"namespace": obj.GetNamespace(),
		"name":      obj.GetName(),
		"hasTTL":    annotations[annotationTTL] != "",
	}).Debug("Checking legacy TTL annotation")
	if ttl, ok := annotations[annotationTTL]; ok {
		duration, err := ParseExtendedDuration(ttl)
		if err != nil {
			logrus.WithError(err).WithField("ttl", ttl).Warn("Invalid TTL format")
			return false, ""
		}

		age := time.Since(obj.GetCreationTimestamp().Time)
		if age > duration {
			return true, fmt.Sprintf("Legacy TTL expired (age: %s, ttl: %s)", age, duration)
		}
		return false, ""
	}

	// Fallback to legacy expiration annotation for backward compatibility
	if expires, ok := annotations[annotationExpires]; ok {
		expirationTime, err := parseExpirationTime(expires)
		if err != nil {
			logrus.WithError(err).WithField("expires", expires).Warn("Invalid expiration format")
			return false, ""
		}

		if time.Now().After(expirationTime) {
			return true, fmt.Sprintf("Legacy expiration time reached (%s)", expires)
		}
		return false, ""
	}

	// Check rules
	logrus.WithFields(logrus.Fields{
		"resource":      obj.GetKind(),
		"namespace":     obj.GetNamespace(),
		"name":          obj.GetName(),
		"hasRuleEngine": j.RuleEngine != nil,
	}).Debug("Checking CEL rules")
	if j.RuleEngine != nil {
		if rule, ttl := j.RuleEngine.Evaluate(obj); rule != nil {
			age := time.Since(obj.GetCreationTimestamp().Time)
			if age > ttl {
				return true, fmt.Sprintf("Rule '%s' matched (age: %s, ttl: %s)", rule.ID, age, ttl)
			}
		}
	}

	logrus.WithFields(logrus.Fields{
		"resource":  obj.GetKind(),
		"namespace": obj.GetNamespace(),
		"name":      obj.GetName(),
	}).Debug("No deletion conditions met - resource will be kept")
	return false, ""
}

func (j *Janitor) getNamespaces(ctx context.Context) ([]string, error) {
	namespaceList, err := j.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	namespaces := make([]string, 0, len(namespaceList.Items))
	for _, ns := range namespaceList.Items {
		namespaces = append(namespaces, ns.Name)
	}

	return namespaces, nil
}

func parseExpirationTime(expires string) (time.Time, error) {
	// Try different formats
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}

	for _, format := range formats {
		t, err := time.Parse(format, expires)
		if err == nil {
			// For date-only format, set time to midnight UTC
			if format == "2006-01-02" {
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			}
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse expiration time: %s", expires)
}

// ParseExtendedDuration parses duration strings with extended units:
// - Standard Go units: h, m, s, ms, us, ns
// - Extended units: d (days), w (weeks), month/months
// Examples: "7d", "2w", "1month", "2w3d", "1month2w3d12h30m"
func ParseExtendedDuration(s string) (time.Duration, error) {
	// First try standard Go duration parsing
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Extended parsing with regex
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(months?|w|d|h|m|s|ms|us|µs|ns)`)
	matches := re.FindAllStringSubmatch(s, -1)

	if len(matches) == 0 {
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}

	var totalDuration time.Duration

	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number in duration: %s", match[1])
		}

		unit := match[2]
		var unitDuration time.Duration

		switch unit {
		case "month", "months":
			// Approximate month as 30 days
			unitDuration = time.Duration(value * 30 * 24 * float64(time.Hour))
		case "w":
			unitDuration = time.Duration(value * 7 * 24 * float64(time.Hour))
		case "d":
			unitDuration = time.Duration(value * 24 * float64(time.Hour))
		case "h":
			unitDuration = time.Duration(value * float64(time.Hour))
		case "m":
			unitDuration = time.Duration(value * float64(time.Minute))
		case "s":
			unitDuration = time.Duration(value * float64(time.Second))
		case "ms":
			unitDuration = time.Duration(value * float64(time.Millisecond))
		case "us", "µs":
			unitDuration = time.Duration(value * float64(time.Microsecond))
		case "ns":
			unitDuration = time.Duration(value * float64(time.Nanosecond))
		default:
			return 0, fmt.Errorf("unknown time unit: %s", unit)
		}

		totalDuration += unitDuration
	}

	// Verify we consumed the entire string (ignoring whitespace)
	consumed := ""
	for _, match := range matches {
		consumed += match[0]
	}
	if strings.ReplaceAll(s, " ", "") != strings.ReplaceAll(consumed, " ", "") {
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}

	return totalDuration, nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// GetNamespaces returns list of namespaces
func (j *Janitor) GetNamespaces(ctx context.Context) ([]string, error) {
	return j.getNamespaces(ctx)
}

// Worker runs a single worker
func (j *Janitor) Worker(ctx context.Context) {
	j.worker(ctx)
}
