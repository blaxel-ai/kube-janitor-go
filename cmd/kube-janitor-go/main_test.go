package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestConfigureKubeAPIClientUsesDefaultRateLimits(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	require.NoError(t, viper.BindPFlags(rootCmd.PersistentFlags()))

	config := &rest.Config{}
	configureKubeAPIClient(config)

	assert.Equal(t, defaultKubeAPIQPS, config.QPS)
	assert.Equal(t, defaultKubeAPIBurst, config.Burst)
}

func TestConfigureKubeAPIClientAllowsClientGoDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("kube-api-qps", 0)
	viper.Set("kube-api-burst", 0)

	config := &rest.Config{}
	configureKubeAPIClient(config)

	assert.Zero(t, config.QPS)
	assert.Zero(t, config.Burst)
}

func TestRunRateLimitsJanitorPaginatedResourceListRequests(t *testing.T) {
	const (
		qps              = 25.0
		burst            = 1
		listPageLimit    = 1
		podCount         = 100
		ttlThreshold     = 50
		maxRuntime       = 15 * time.Second
		ignoredNamespace = "ignored-namespace"
	)

	includedNamespaces := []string{
		"workspace-0",
		"workspace-1",
		"workspace-2",
		"workspace-3",
		"workspace-4",
		"workspace-5",
	}

	podDeletePath := regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/pods/([^/]+)$`)

	var mu sync.Mutex
	var podListRequests []podListRequest
	var deletedPodNames []string
	podCounter := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api":
			_, _ = w.Write([]byte(`{
				"kind": "APIVersions",
				"apiVersion": "v1",
				"versions": ["v1"],
				"serverAddressByClientCIDRs": []
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/apis":
			_, _ = w.Write([]byte(`{
				"kind": "APIGroupList",
				"apiVersion": "v1",
				"groups": []
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1":
			_, _ = w.Write([]byte(`{
				"kind": "APIResourceList",
				"apiVersion": "v1",
				"groupVersion": "v1",
				"resources": [
					{
						"name": "pods",
						"singularName": "",
						"namespaced": true,
						"kind": "Pod",
						"verbs": ["get", "list", "watch", "delete"]
					}
				]
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pods":
			mu.Lock()
			podListRequests = append(podListRequests, podListRequest{
				timestamp:     time.Now(),
				limit:         r.URL.Query().Get("limit"),
				continueToken: r.URL.Query().Get("continue"),
			})
			requestCount := len(podListRequests)
			elapsedSinceFirstRequest := time.Since(podListRequests[0].timestamp)
			podIDs := make([]int, listPageLimit)
			for i := range podIDs {
				podIDs[i] = podCounter + i
			}
			podCounter += listPageLimit
			mu.Unlock()

			if elapsedSinceFirstRequest > maxRuntime {
				http.Error(w, "pod list requests exceeded maximum runtime", http.StatusRequestTimeout)
				return
			}

			expectedContinueToken := ""
			if requestCount > 1 {
				expectedContinueToken = fmt.Sprintf("page-%d", requestCount-1)
			}
			if r.URL.Query().Get("continue") != expectedContinueToken {
				http.Error(w, "unexpected continue token", http.StatusBadRequest)
				return
			}
			if requestCount > podCount {
				http.Error(w, "too many pod list requests", http.StatusBadRequest)
				return
			}

			nextContinueToken := ""
			if requestCount < podCount {
				nextContinueToken = fmt.Sprintf("page-%d", requestCount)
			}
			_, _ = w.Write([]byte(podListJSON(nextContinueToken, includedNamespaces, ignoredNamespace, podIDs, ttlThreshold)))
		case r.Method == http.MethodDelete && podDeletePath.MatchString(r.URL.Path):
			match := podDeletePath.FindStringSubmatch(r.URL.Path)
			mu.Lock()
			deletedPodNames = append(deletedPodNames, match[2])
			mu.Unlock()
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
		case strings.Contains(r.URL.Path, "/events"):
			_, _ = io.Copy(io.Discard, r.Body)
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			_, _ = w.Write([]byte(`{"kind":"Event","apiVersion":"v1","metadata":{"name":"ev","namespace":"default","resourceVersion":"1"},"involvedObject":{},"reason":"x","message":"x","type":"Normal"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	kubeconfig := writeTestKubeconfig(t, server.URL)

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("once", true)
	viper.Set("dry-run", false)
	viper.Set("metrics-port", 0)
	viper.Set("kubeconfig", kubeconfig)
	viper.Set("include-resources", []string{"pods"})
	viper.Set("include-namespaces", includedNamespaces)
	viper.Set("exclude-resources", []string{})
	viper.Set("exclude-namespaces", []string{})
	viper.Set("max-workers", 1)
	viper.Set("list-page-limit", int64(listPageLimit))
	viper.Set("log-level", "info")
	viper.Set("kube-api-qps", qps)
	viper.Set("kube-api-burst", burst)

	initConfig()
	require.NoError(t, run(rootCmd, nil))

	mu.Lock()
	gotPodListRequests := append([]podListRequest(nil), podListRequests...)
	gotDeletedPodNames := append([]string(nil), deletedPodNames...)
	mu.Unlock()

	require.Len(t, gotPodListRequests, podCount)
	for i, request := range gotPodListRequests {
		assert.Equal(t, fmt.Sprintf("%d", listPageLimit), request.limit)

		if i == 0 {
			assert.Empty(t, request.continueToken)
		} else {
			assert.Equal(t, fmt.Sprintf("page-%d", i), request.continueToken)
		}
	}

	sort.Slice(gotPodListRequests, func(i, j int) bool {
		return gotPodListRequests[i].timestamp.Before(gotPodListRequests[j].timestamp)
	})

	span := gotPodListRequests[len(gotPodListRequests)-1].timestamp.Sub(gotPodListRequests[0].timestamp)
	expectedMinimum := time.Duration(float64(podCount-burst) / qps * float64(time.Second))

	assert.GreaterOrEqual(t, span, expectedMinimum-200*time.Millisecond)
	assert.LessOrEqual(t, span, maxRuntime)

	var expectedDeletions []string
	for id := 0; id < podCount; id++ {
		if id%10 != 0 && id >= ttlThreshold {
			expectedDeletions = append(expectedDeletions, fmt.Sprintf("pod-%d", id))
		}
	}
	assert.ElementsMatch(t, expectedDeletions, gotDeletedPodNames,
		"only pods past the TTL threshold and not in the ignored namespace should have been deleted")

	for id := 0; id < podCount; id += 10 {
		assert.NotContains(t, gotDeletedPodNames, fmt.Sprintf("pod-%d", id),
			"pod-%d is in the ignored namespace and must never be deleted", id)
	}
}

type podListRequest struct {
	timestamp     time.Time
	limit         string
	continueToken string
}

func writeTestKubeconfig(t *testing.T, serverURL string) string {
	t.Helper()

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`, serverURL)

	require.NoError(t, os.WriteFile(kubeconfig, []byte(content), 0600))
	return kubeconfig
}

func podListJSON(continueToken string, includedNamespaces []string, ignoredNamespace string, podIDs []int, ttlThreshold int) string {
	items := make([]string, 0, len(podIDs))
	for _, podID := range podIDs {
		var namespace string
		if podID%10 == 0 {
			namespace = ignoredNamespace
		} else {
			namespace = includedNamespaces[podID%len(includedNamespaces)]
		}
		ttl := "100w"
		if podID >= ttlThreshold {
			ttl = "1ns"
		}
		items = append(items, fmt.Sprintf(`{
			"apiVersion": "v1",
			"kind": "Pod",
			"metadata": {
				"name": "pod-%d",
				"namespace": %q,
				"creationTimestamp": "2026-01-01T00:00:00Z",
				"annotations": {"janitor/ttl": %q}
			}
		}`, podID, namespace, ttl))
	}

	return fmt.Sprintf(`{
		"kind": "PodList",
		"apiVersion": "v1",
		"metadata": {
			"continue": %q
		},
		"items": [%s]
	}`, continueToken, strings.Join(items, ","))
}
