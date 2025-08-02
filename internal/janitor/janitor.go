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

	"github.com/blaxel-ai/kube-janitor-go/internal/metrics"
	"github.com/blaxel-ai/kube-janitor-go/internal/rules"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
)

const (
	annotationTTL     = "janitor/ttl"
	annotationExpires = "janitor/expires"
)

// Config holds the janitor configuration
type Config struct {
	DryRun            bool
	Interval          time.Duration
	Once              bool
	IncludeResources  []string
	ExcludeResources  []string
	IncludeNamespaces []string
	ExcludeNamespaces []string
	RulesFile         string
	MaxWorkers        int
}

// Janitor is the main cleanup controller
type Janitor struct {
	Clientset       kubernetes.Interface
	DynamicClient   dynamic.Interface
	DiscoveryClient discovery.DiscoveryInterface
	Config          Config
	RuleEngine      *rules.Engine
	ResourceFilter  *ResourceFilter
	WorkQueue       chan WorkItem
	wg              sync.WaitGroup
	EventRecorder   record.EventRecorder
}

// WorkItem represents an item to be processed
type WorkItem struct {
	Resource  schema.GroupVersionResource
	Namespace string
	Name      string
	Obj       *unstructured.Unstructured
}

// New creates a new Janitor instance
func New(clientset kubernetes.Interface, restConfig *rest.Config, config Config) (*Janitor, error) {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
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

	return &Janitor{
		Clientset:       clientset,
		DynamicClient:   dynamicClient,
		DiscoveryClient: discoveryClient,
		Config:          config,
		RuleEngine:      ruleEngine,
		ResourceFilter:  resourceFilter,
		WorkQueue:       make(chan WorkItem, 1000),
		wg:              sync.WaitGroup{},
		EventRecorder:   recorder,
	}, nil
}

// Run starts the janitor
func (j *Janitor) Run(ctx context.Context) error {
	logrus.Info("Starting janitor")

	// Start workers
	for i := 0; i < j.Config.MaxWorkers; i++ {
		j.wg.Add(1)
		go j.worker(ctx)
	}

	// Run cleanup loop
	if j.Config.Once {
		if err := j.cleanup(ctx); err != nil {
			metrics.Errors.WithLabelValues("cleanup").Inc()
			return err
		}
	} else {
		ticker := time.NewTicker(j.Config.Interval)
		defer ticker.Stop()

		// Run immediately
		if err := j.cleanup(ctx); err != nil {
			logrus.WithError(err).Error("Cleanup failed")
			metrics.Errors.WithLabelValues("cleanup").Inc()
		}

		for {
			select {
			case <-ticker.C:
				if err := j.cleanup(ctx); err != nil {
					logrus.WithError(err).Error("Cleanup failed")
					metrics.Errors.WithLabelValues("cleanup").Inc()
				}
			case <-ctx.Done():
				logrus.Info("Shutting down janitor")
				close(j.WorkQueue)
				j.wg.Wait()
				return nil
			}
		}
	}

	close(j.WorkQueue)
	j.wg.Wait()
	return nil
}

func (j *Janitor) cleanup(ctx context.Context) error {
	logrus.Debug("Starting cleanup run")
	timer := prometheus.NewTimer(metrics.CleanupDuration)
	defer timer.ObserveDuration()

	// Get all resource types
	resources, err := j.DiscoveryClient.ServerPreferredResources()
	if err != nil {
		return fmt.Errorf("failed to discover resources: %w", err)
	}

	// Process each resource type
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
			// Skip resources that can't be listed or deleted
			if !contains(resource.Verbs, "list") || !contains(resource.Verbs, "delete") {
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

			// Process namespaced resources
			if resource.Namespaced {
				namespaces, err := j.getNamespaces(ctx)
				if err != nil {
					logrus.WithError(err).Error("Failed to list namespaces")
					metrics.Errors.WithLabelValues("list_namespaces").Inc()
					continue
				}

				for _, ns := range namespaces {
					if !j.ResourceFilter.ShouldProcessNamespace(ns) {
						continue
					}

					if err := j.processResources(ctx, gvr, ns); err != nil {
						logrus.WithError(err).WithFields(logrus.Fields{
							"resource":  resource.Name,
							"namespace": ns,
						}).Error("Failed to process resources")
						metrics.Errors.WithLabelValues("process_resources").Inc()
					}
				}
			} else {
				// Process cluster-scoped resources
				if err := j.processResources(ctx, gvr, ""); err != nil {
					logrus.WithError(err).WithField("resource", resource.Name).Error("Failed to process resources")
					metrics.Errors.WithLabelValues("process_resources").Inc()
				}
			}
		}
	}

	logrus.Info("Cleanup run completed")
	return nil
}

func (j *Janitor) processResources(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error {
	var resourceInterface dynamic.ResourceInterface
	if namespace != "" {
		resourceInterface = j.DynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resourceInterface = j.DynamicClient.Resource(gvr)
	}

	list, err := resourceInterface.List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, item := range list.Items {
		obj := item
		// Track evaluated resources
		metrics.ResourcesEvaluated.WithLabelValues(gvr.Resource, namespace).Inc()

		j.WorkQueue <- WorkItem{
			Resource:  gvr,
			Namespace: namespace,
			Name:      obj.GetName(),
			Obj:       &obj,
		}
	}

	return nil
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

	// Check if resource should be deleted
	shouldDelete, reason := j.shouldDelete(item.Obj)
	if !shouldDelete {
		return
	}

	logger.WithField("reason", reason).Info("Resource marked for deletion")

	// Create a reference to the object for the event
	ref := &corev1.ObjectReference{
		APIVersion: item.Resource.Group + "/" + item.Resource.Version,
		Kind:       item.Obj.GetKind(),
		Namespace:  item.Namespace,
		Name:       item.Name,
		UID:        item.Obj.GetUID(),
	}

	if j.Config.DryRun {
		logger.Info("DRY RUN: Would delete resource")
		// Create event for dry-run
		eventMessage := fmt.Sprintf("DRY RUN: Would delete %s %s/%s - %s",
			item.Resource.Resource, item.Namespace, item.Name, reason)
		j.EventRecorder.Event(ref, corev1.EventTypeNormal, "DryRunDeletion", eventMessage)
		return
	}

	// Delete the resource
	var resourceInterface dynamic.ResourceInterface
	if item.Namespace != "" {
		resourceInterface = j.DynamicClient.Resource(item.Resource).Namespace(item.Namespace)
	} else {
		resourceInterface = j.DynamicClient.Resource(item.Resource)
	}

	err := resourceInterface.Delete(ctx, item.Name, metav1.DeleteOptions{})
	if err != nil {
		logger.WithError(err).Error("Failed to delete resource")
		metrics.Errors.WithLabelValues("delete_resource").Inc()
		// Create event for failed deletion
		eventMessage := fmt.Sprintf("Failed to delete %s %s/%s: %v",
			item.Resource.Resource, item.Namespace, item.Name, err)
		j.EventRecorder.Event(ref, corev1.EventTypeWarning, "DeletionFailed", eventMessage)
		return
	}

	logger.Info("Resource deleted")
	metrics.ResourcesDeleted.WithLabelValues(item.Resource.Resource, item.Namespace, reason).Inc()

	// Create event for successful deletion
	eventMessage := fmt.Sprintf("Deleted %s %s/%s - %s",
		item.Resource.Resource, item.Namespace, item.Name, reason)
	j.EventRecorder.Event(ref, corev1.EventTypeNormal, "ResourceDeleted", eventMessage)
}

func (j *Janitor) shouldDelete(obj *unstructured.Unstructured) (bool, string) {
	// Check TTL annotation
	if ttl, ok := obj.GetAnnotations()[annotationTTL]; ok {
		duration, err := ParseExtendedDuration(ttl)
		if err != nil {
			logrus.WithError(err).WithField("ttl", ttl).Warn("Invalid TTL format")
			return false, ""
		}

		age := time.Since(obj.GetCreationTimestamp().Time)
		if age > duration {
			return true, fmt.Sprintf("TTL expired (age: %s, ttl: %s)", age, duration)
		}
		return false, ""
	}

	// Check expiration annotation
	if expires, ok := obj.GetAnnotations()[annotationExpires]; ok {
		expirationTime, err := parseExpirationTime(expires)
		if err != nil {
			logrus.WithError(err).WithField("expires", expires).Warn("Invalid expiration format")
			return false, ""
		}

		if time.Now().After(expirationTime) {
			return true, fmt.Sprintf("Expiration time reached (%s)", expires)
		}
		return false, ""
	}

	// Check rules
	if j.RuleEngine != nil {
		if rule, ttl := j.RuleEngine.Evaluate(obj); rule != nil {
			age := time.Since(obj.GetCreationTimestamp().Time)
			if age > ttl {
				return true, fmt.Sprintf("Rule '%s' matched (age: %s, ttl: %s)", rule.ID, age, ttl)
			}
		}
	}

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
