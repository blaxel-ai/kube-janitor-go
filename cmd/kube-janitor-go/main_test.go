package main

import (
	"fmt"
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

func TestRunRateLimitsJanitorResourceListRequests(t *testing.T) {
	const (
		qps   = 4.0
		burst = 1
	)

	allNamespaces := []string{
		"workspace-0",
		"workspace-1",
		"workspace-2",
		"workspace-3",
		"workspace-4",
		"workspace-5",
		"ignored-workspace",
	}
	includedNamespaces := allNamespaces[:6]

	var mu sync.Mutex
	var podListTimestamps []time.Time
	var podListNamespaces []string

	podListPath := regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/pods$`)
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces":
			_, _ = w.Write([]byte(namespaceListJSON(allNamespaces)))
		case r.Method == http.MethodGet && podListPath.MatchString(r.URL.Path):
			namespace := podListPath.FindStringSubmatch(r.URL.Path)[1]
			mu.Lock()
			podListTimestamps = append(podListTimestamps, time.Now())
			podListNamespaces = append(podListNamespaces, namespace)
			mu.Unlock()

			_, _ = w.Write([]byte(`{
				"kind": "PodList",
				"apiVersion": "v1",
				"items": []
			}`))
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
	viper.Set("dry-run", true)
	viper.Set("metrics-port", 0)
	viper.Set("kubeconfig", kubeconfig)
	viper.Set("include-resources", []string{"pods"})
	viper.Set("include-namespaces", includedNamespaces)
	viper.Set("exclude-resources", []string{})
	viper.Set("exclude-namespaces", []string{})
	viper.Set("max-workers", 1)
	viper.Set("log-level", "info")
	viper.Set("kube-api-qps", qps)
	viper.Set("kube-api-burst", burst)

	initConfig()
	require.NoError(t, run(rootCmd, nil))

	mu.Lock()
	gotTimestamps := append([]time.Time(nil), podListTimestamps...)
	gotNamespaces := append([]string(nil), podListNamespaces...)
	mu.Unlock()

	require.Len(t, gotTimestamps, len(includedNamespaces))
	assert.ElementsMatch(t, includedNamespaces, gotNamespaces)
	assert.NotContains(t, gotNamespaces, "ignored-workspace")

	sort.Slice(gotTimestamps, func(i, j int) bool {
		return gotTimestamps[i].Before(gotTimestamps[j])
	})

	span := gotTimestamps[len(gotTimestamps)-1].Sub(gotTimestamps[0])
	expectedMinimum := time.Duration(float64(len(includedNamespaces)-burst) / qps * float64(time.Second))

	assert.GreaterOrEqual(t, span, expectedMinimum-200*time.Millisecond)
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

func namespaceListJSON(namespaces []string) string {
	items := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		items = append(items, fmt.Sprintf(`{"metadata":{"name":%q}}`, namespace))
	}

	return fmt.Sprintf(`{
		"kind": "NamespaceList",
		"apiVersion": "v1",
		"items": [%s]
	}`, strings.Join(items, ","))
}
