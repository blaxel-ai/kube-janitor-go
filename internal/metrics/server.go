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
		[]string{"resource", "namespace", "reason"},
	)

	// ResourcesEvaluated is a counter for evaluated resources
	ResourcesEvaluated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_evaluated_total",
			Help: "Total number of resources evaluated",
		},
		[]string{"resource", "namespace"},
	)

	// CleanupDuration is a histogram for cleanup run durations
	CleanupDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kube_janitor_cleanup_duration_seconds",
			Help:    "Histogram of cleanup run durations",
			Buckets: prometheus.DefBuckets,
		},
	)

	// Errors is a counter for errors
	Errors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_errors_total",
			Help: "Total number of errors encountered",
		},
		[]string{"type"},
	)

	// ResourcesSkipped is a counter for resources skipped due to sharding
	ResourcesSkipped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_skipped_total",
			Help: "Total number of resources skipped due to sharding (handled by other instances)",
		},
		[]string{"resource", "namespace"},
	)

	// ShardingPeers is a gauge for the number of peers in the sharding cluster
	ShardingPeers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "kube_janitor_sharding_peers",
			Help: "Number of peer janitor instances in the sharding cluster",
		},
	)
)

func init() {
	// Register metrics
	prometheus.MustRegister(ResourcesDeleted)
	prometheus.MustRegister(ResourcesEvaluated)
	prometheus.MustRegister(CleanupDuration)
	prometheus.MustRegister(Errors)
	prometheus.MustRegister(ResourcesSkipped)
	prometheus.MustRegister(ShardingPeers)
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
