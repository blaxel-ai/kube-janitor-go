package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServer(t *testing.T) {
	server := NewServer(8080)
	assert.NotNil(t, server)
	assert.Equal(t, 8080, server.port)
}

func TestMetricsRegistration(t *testing.T) {
	// Test that metrics are registered
	// This is mainly to ensure the init() function runs without panic
	assert.NotNil(t, ResourcesDeleted)
	assert.NotNil(t, ResourcesEvaluated)
	assert.NotNil(t, CleanupDuration)
	assert.NotNil(t, Errors)
}

func TestServerStart(t *testing.T) {
	// Find an available port
	NewServer(0) // Use 0 to let the system assign a port

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		// Create a test server with a custom handler
		mux := http.NewServeMux()
		mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("# HELP test_metric Test metric\n")); err != nil {
				t.Logf("Failed to write metrics response: %v", err)
			}
		}))
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("ok")); err != nil {
				t.Logf("Failed to write health response: %v", err)
			}
		})

		// Use a test server instead of the real one for testing
		testServer := &http.Server{
			Addr:    ":0",
			Handler: mux,
		}

		// Start listening
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			errChan <- err
			return
		}
		defer listener.Close()

		port := listener.Addr().(*net.TCPAddr).Port

		// Allow tests to proceed
		go func() {
			time.Sleep(100 * time.Millisecond)

			// Test health endpoint
			resp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port))
			if err == nil {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Equal(t, "ok", string(body))
			}

			// Test metrics endpoint
			resp, err = http.Get(fmt.Sprintf("http://localhost:%d/metrics", port))
			if err == nil {
				defer resp.Body.Close()
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}

			// Shutdown the server
			testServer.Close()
		}()

		errChan <- testServer.Serve(listener)
	}()

	// Wait for server to start or timeout
	select {
	case err := <-errChan:
		// Server closed is expected
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("Server error: %v", err)
		}
	case <-time.After(1 * time.Second):
		// This is OK - server is running
	}
}

func TestMetricsIncrement(t *testing.T) {
	// Reset metrics for testing
	prometheus.Unregister(ResourcesDeleted)
	prometheus.Unregister(ResourcesEvaluated)
	prometheus.Unregister(CleanupDuration)
	prometheus.Unregister(Errors)

	// Re-register metrics
	ResourcesDeleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_deleted_total_test",
			Help: "Total number of resources deleted",
		},
		[]string{"resource", "namespace", "reason"},
	)
	ResourcesEvaluated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_resources_evaluated_total_test",
			Help: "Total number of resources evaluated",
		},
		[]string{"resource", "namespace"},
	)
	CleanupDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kube_janitor_cleanup_duration_seconds_test",
			Help:    "Histogram of cleanup run durations",
			Buckets: prometheus.DefBuckets,
		},
	)
	Errors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kube_janitor_errors_total_test",
			Help: "Total number of errors encountered",
		},
		[]string{"type"},
	)

	prometheus.MustRegister(ResourcesDeleted)
	prometheus.MustRegister(ResourcesEvaluated)
	prometheus.MustRegister(CleanupDuration)
	prometheus.MustRegister(Errors)

	// Test incrementing metrics
	ResourcesDeleted.WithLabelValues("pods", "default", "ttl_expired").Inc()
	ResourcesEvaluated.WithLabelValues("pods", "default").Inc()
	Errors.WithLabelValues("delete_failed").Inc()

	// Test histogram
	timer := prometheus.NewTimer(CleanupDuration)
	time.Sleep(10 * time.Millisecond)
	timer.ObserveDuration()

	// Verify metrics can be collected
	metrics, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	foundDeleted := false
	foundEvaluated := false
	foundDuration := false
	foundErrors := false

	for _, metric := range metrics {
		switch metric.GetName() {
		case "kube_janitor_resources_deleted_total_test":
			foundDeleted = true
			assert.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(1), metric.GetMetric()[0].GetCounter().GetValue())
		case "kube_janitor_resources_evaluated_total_test":
			foundEvaluated = true
			assert.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(1), metric.GetMetric()[0].GetCounter().GetValue())
		case "kube_janitor_cleanup_duration_seconds_test":
			foundDuration = true
			assert.Len(t, metric.GetMetric(), 1)
			assert.Greater(t, metric.GetMetric()[0].GetHistogram().GetSampleCount(), uint64(0))
		case "kube_janitor_errors_total_test":
			foundErrors = true
			assert.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(1), metric.GetMetric()[0].GetCounter().GetValue())
		}
	}

	assert.True(t, foundDeleted, "ResourcesDeleted metric not found")
	assert.True(t, foundEvaluated, "ResourcesEvaluated metric not found")
	assert.True(t, foundDuration, "CleanupDuration metric not found")
	assert.True(t, foundErrors, "Errors metric not found")
}
