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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

const (
	deletionReasonDeleteIfMaxAge = "delete_if_max_age"
	deletionReasonDeleteIfIdle   = "delete_if_idle"
	deletionReasonDeleteIfDate   = "delete_if_date"
	deletionReasonLegacyTTL      = "legacy_ttl"
	deletionReasonLegacyExpires  = "legacy_expires"
	deletionReasonRuleTTL        = "rule_ttl"
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
	ListPageLimit     int64
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

type deletionDecision struct {
	shouldDelete    bool
	reason          string
	detailedMessage string
	ttlDeadline     time.Time
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
		WorkQueue:       make(chan WorkItem, 2000),
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

			if err := j.processResources(ctx, gvr, resource.Namespaced); err != nil {
				logrus.WithError(err).WithField("resource", resource.Name).Error("Failed to process resources")
				metrics.Errors.WithLabelValues("process_resources").Inc()
			}
		}
	}

	logrus.Info("Cleanup run completed")
	return nil
}

func (j *Janitor) processResources(ctx context.Context, gvr schema.GroupVersionResource, namespaced bool) error {
	resourceInterface := j.DynamicClient.Resource(gvr)
	listOptions := metav1.ListOptions{Limit: j.Config.ListPageLimit}
	restartedAfterExpiry := false

	for {
		list, err := resourceInterface.List(ctx, listOptions)
		if err != nil {
			if apierrors.IsResourceExpired(err) && !restartedAfterExpiry {
				logrus.WithError(err).WithField("resource", gvr.Resource).Warn("Continue token expired, restarting paginated list")
				listOptions.Continue = ""
				restartedAfterExpiry = true
				continue
			}
			return err
		}

		for _, item := range list.Items {
			obj := item
			namespace := obj.GetNamespace()
			if namespaced && !j.ResourceFilter.ShouldProcessNamespace(namespace) {
				continue
			}

			// Track evaluated resources without namespace/workspace labels to keep metric cardinality bounded.
			metrics.ResourcesEvaluated.WithLabelValues(gvr.Resource).Inc()

			j.WorkQueue <- WorkItem{
				Resource:  gvr,
				Namespace: namespace,
				Name:      obj.GetName(),
				Obj:       &obj,
			}
		}

		if list.GetContinue() == "" {
			return nil
		}
		listOptions.Continue = list.GetContinue()
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

	// Check if resource should be deleted
	decision := j.shouldDelete(item.Obj)
	if !decision.shouldDelete {
		return
	}

	logger.WithFields(logrus.Fields{
		"reason":          decision.reason,
		"detailedMessage": decision.detailedMessage,
	}).Info("Resource marked for deletion")

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
			item.Resource.Resource, item.Namespace, item.Name, decision.detailedMessage)
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

	err := resourceInterface.Delete(ctx, item.Name, deleteOptionsForObject(item.Obj))
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
	metrics.ResourcesDeleted.WithLabelValues(item.Resource.Resource, decision.reason).Inc()
	if !decision.ttlDeadline.IsZero() {
		ttlLag := time.Since(decision.ttlDeadline)
		if ttlLag < 0 {
			ttlLag = 0
		}
		metrics.TTLDeletionLag.WithLabelValues(item.Resource.Resource, decision.reason).Observe(ttlLag.Seconds())
	}

	// Create event for successful deletion
	eventMessage := fmt.Sprintf("Deleted %s %s/%s - %s",
		item.Resource.Resource, item.Namespace, item.Name, decision.detailedMessage)
	j.EventRecorder.Event(ref, corev1.EventTypeNormal, "ResourceDeleted", eventMessage)
}

func deleteOptionsForObject(obj *unstructured.Unstructured) metav1.DeleteOptions {
	uid := obj.GetUID()
	return metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &uid,
		},
	}
}

// evaluateDeleteIfMaxAge checks if resource should be deleted based on max age
func (j *Janitor) evaluateDeleteIfMaxAge(value string, obj *unstructured.Unstructured) (bool, string, time.Time, error) {
	duration, err := ParseExtendedDuration(value)
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("invalid duration format: %w", err)
	}

	createdAt := obj.GetCreationTimestamp().Time
	now := time.Now()
	age := now.Sub(createdAt)
	ttlDeadline := createdAt.Add(duration)
	if now.After(ttlDeadline) {
		return true, fmt.Sprintf("Delete max-age policy triggered (age: %s, max-age: %s)", age, duration), ttlDeadline, nil
	}
	return false, "", time.Time{}, nil
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
func (j *Janitor) evaluateDeleteIfIdle(value string, obj *unstructured.Unstructured) (bool, string, time.Time, error) {
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
		return false, "", time.Time{}, nil
	}

	lastUsedAt, err := time.Parse(time.RFC3339, lastUsedAtStr)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"resource":      obj.GetKind(),
			"namespace":     obj.GetNamespace(),
			"name":          obj.GetName(),
			"lastUsedAtStr": lastUsedAtStr,
		}).Debug("Failed to parse lastUsedAt timestamp")
		return false, "", time.Time{}, fmt.Errorf("invalid lastUsedAt format: %w", err)
	}

	duration, err := ParseExtendedDuration(value)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"resource":  obj.GetKind(),
			"namespace": obj.GetNamespace(),
			"name":      obj.GetName(),
			"value":     value,
		}).Debug("Failed to parse duration")
		return false, "", time.Time{}, fmt.Errorf("invalid duration format: %w", err)
	}

	now := time.Now()
	idleTime := now.Sub(lastUsedAt)
	ttlDeadline := lastUsedAt.Add(duration)
	logrus.WithFields(logrus.Fields{
		"resource":     obj.GetKind(),
		"namespace":    obj.GetNamespace(),
		"name":         obj.GetName(),
		"idleTime":     idleTime,
		"duration":     duration,
		"shouldDelete": now.After(ttlDeadline),
		"lastUsedAt":   lastUsedAt,
	}).Debug("Evaluating idle time")

	if now.After(ttlDeadline) {
		return true, fmt.Sprintf("Delete idle policy triggered (idle: %s, max-idle: %s)", idleTime, duration), ttlDeadline, nil
	}
	return false, "", time.Time{}, nil
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

// shouldDelete returns a deletion reason that is used as a Prometheus metric label;
// therefore reason must have a finite set of possible values. Put dynamic context
// such as ages, TTLs, timestamps, annotation keys, and rule IDs in detailedMessage.
func (j *Janitor) shouldDelete(obj *unstructured.Unstructured) deletionDecision {
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
		shouldDelete, detailedMessage, ttlDeadline, err := j.evaluateDeleteIfMaxAge(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-max-age policy")
			continue
		}
		if shouldDelete {
			return deletionDecision{
				shouldDelete:    true,
				reason:          deletionReasonDeleteIfMaxAge,
				detailedMessage: fmt.Sprintf("%s (annotation: %s)", detailedMessage, annotationKey),
				ttlDeadline:     ttlDeadline,
			}
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
		shouldDelete, detailedMessage, ttlDeadline, err := j.evaluateDeleteIfIdle(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-idle policy")
			continue
		}
		if shouldDelete {
			return deletionDecision{
				shouldDelete:    true,
				reason:          deletionReasonDeleteIfIdle,
				detailedMessage: fmt.Sprintf("%s (annotation: %s)", detailedMessage, annotationKey),
				ttlDeadline:     ttlDeadline,
			}
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
		shouldDelete, detailedMessage, err := j.evaluateDeleteIfDate(annotationValue, obj)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"annotation": annotationKey,
				"value":      annotationValue,
			}).Warn("Failed to evaluate delete-if-date policy")
			continue
		}
		if shouldDelete {
			return deletionDecision{
				shouldDelete:    true,
				reason:          deletionReasonDeleteIfDate,
				detailedMessage: fmt.Sprintf("%s (annotation: %s)", detailedMessage, annotationKey),
			}
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
			return deletionDecision{}
		}

		createdAt := obj.GetCreationTimestamp().Time
		now := time.Now()
		age := now.Sub(createdAt)
		ttlDeadline := createdAt.Add(duration)
		if now.After(ttlDeadline) {
			return deletionDecision{
				shouldDelete:    true,
				reason:          deletionReasonLegacyTTL,
				detailedMessage: fmt.Sprintf("Legacy TTL expired (age: %s, ttl: %s)", age, duration),
				ttlDeadline:     ttlDeadline,
			}
		}
		return deletionDecision{}
	}

	// Fallback to legacy expiration annotation for backward compatibility
	if expires, ok := annotations[annotationExpires]; ok {
		expirationTime, err := parseExpirationTime(expires)
		if err != nil {
			logrus.WithError(err).WithField("expires", expires).Warn("Invalid expiration format")
			return deletionDecision{}
		}

		if time.Now().After(expirationTime) {
			return deletionDecision{
				shouldDelete:    true,
				reason:          deletionReasonLegacyExpires,
				detailedMessage: fmt.Sprintf("Legacy expiration time reached (%s)", expires),
			}
		}
		return deletionDecision{}
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
			createdAt := obj.GetCreationTimestamp().Time
			now := time.Now()
			age := now.Sub(createdAt)
			ttlDeadline := createdAt.Add(ttl)
			if now.After(ttlDeadline) {
				return deletionDecision{
					shouldDelete:    true,
					reason:          deletionReasonRuleTTL,
					detailedMessage: fmt.Sprintf("Rule '%s' matched (age: %s, ttl: %s)", rule.ID, age, ttl),
					ttlDeadline:     ttlDeadline,
				}
			}
		}
	}

	logrus.WithFields(logrus.Fields{
		"resource":  obj.GetKind(),
		"namespace": obj.GetNamespace(),
		"name":      obj.GetName(),
	}).Debug("No deletion conditions met - resource will be kept")
	return deletionDecision{}
}

func (j *Janitor) getNamespaces(ctx context.Context) ([]string, error) {
	var namespaces []string
	listOptions := metav1.ListOptions{Limit: j.Config.ListPageLimit}

	for {
		namespaceList, err := j.Clientset.CoreV1().Namespaces().List(ctx, listOptions)
		if err != nil {
			return nil, err
		}

		for _, ns := range namespaceList.Items {
			namespaces = append(namespaces, ns.Name)
		}

		if namespaceList.GetContinue() == "" {
			return namespaces, nil
		}
		listOptions.Continue = namespaceList.GetContinue()
	}
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
