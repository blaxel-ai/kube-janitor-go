package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var (
	// ResourcesDeleted is a counter for deleted resources
	ResourcesDeleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_deleted_total",
			Help: "Total number of resources deleted",
		},
		[]string{"resource", "reason"},
	)

	// ResourcesEvaluated is a counter for evaluated resources
	ResourcesEvaluated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_evaluated_total",
			Help: "Total number of resources evaluated",
		},
		[]string{"resource"},
	)

	// CleanupDuration is a histogram for cleanup run durations
	CleanupDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kube_janitor_cleanup_duration_seconds",
			Help:    "Histogram of cleanup run durations",
			Buckets: prometheus.DefBuckets,
		},
	)

	// TTLDeletionLag is a histogram for the delay between TTL expiry and deletion
	TTLDeletionLag = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kube_janitor_ttl_deletion_lag_seconds",
			Help:    "Histogram of delay between TTL expiration time and successful resource deletion",
			Buckets: prometheus.ExponentialBuckets(1, 2, 20),
		},
		[]string{"resource", "reason"},
	)

	// Errors is a counter for errors
	Errors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_errors_total",
			Help: "Total number of errors encountered",
		},
		[]string{"type"},
	)
)

func init() {
	// Register metrics
	prometheus.MustRegister(ResourcesDeleted)
	prometheus.MustRegister(ResourcesEvaluated)
	prometheus.MustRegister(CleanupDuration)
	prometheus.MustRegister(TTLDeletionLag)
	prometheus.MustRegister(Errors)
}

// Server represents the metrics server
type Server struct {
	port int
}

// NewServer creates a new metrics server
func NewServer(port int) *Server {
	return &Server{
		port: port,
	}
}

// Start starts the metrics server
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logrus.Errorf("Failed to write health check response: %v", err)
		}
	})

	addr := fmt.Sprintf(":%d", s.port)
	logrus.Infof("Starting metrics server on %s", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return server.ListenAndServe()
}
