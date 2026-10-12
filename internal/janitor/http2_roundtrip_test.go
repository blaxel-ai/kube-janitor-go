package janitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestJanitorNew_HTTP2RoundTrip exercises the exact client construction
// cmd/kube-janitor-go/main.go performs (kubernetes.NewForConfig(config),
// then janitor.New(clientset, config, janitorConfig), which itself builds
// a dynamic.Interface and a discovery.DiscoveryInterface from the same
// *rest.Config) against a fake Kubernetes API server on a loopback
// httptest.Server with HTTP/2 enabled.
//
// golang.org/x/net was bumped 0.38.0 -> 0.55.0 in this change
// (GHSA-5cv4-jp36-h3mw / CVE-2026-25680, an unbounded-CPU DoS in
// golang.org/x/net/html). This repo never imports x/net/html --
// go mod why -m golang.org/x/net shows the module is reached only via
// k8s.io/client-go/rest -> golang.org/x/net/http2, so the specific
// vulnerable parser is not on this repo's call path. The bump forces a
// go.mod language-minimum bump (x/net 0.55.0 requires go >= 1.25.0,
// updated alongside this test in the Dockerfile and CI workflows), so this
// test exists to prove the bump did not break the surface that *is*
// reachable: client-go's real HTTP/2 transport, which
// golang.org/x/net/http2 implements, exercised through both the typed
// clientset (a List call) and the discovery client (ServerVersion, which
// New() itself constructs) -- not through the fake in-memory clientsets
// the rest of this package's tests use, which never touch the network.
func TestJanitorNew_HTTP2RoundTrip(t *testing.T) {
	var sawHTTP2 atomic.Bool

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			sawHTTP2.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(version.Info{Major: "1", Minor: "31", GitVersion: "v1.31.0-fake"})
		case "/api/v1/pods":
			list := &corev1.PodList{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
				Items: []corev1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "smoke-pod", Namespace: "default"}},
				},
			}
			_ = json.NewEncoder(w).Encode(list)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	restCfg := &rest.Config{
		Host: srv.URL,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // test-only: self-signed httptest cert
		},
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	require.NoError(t, err)

	// Same call cmd/kube-janitor-go/main.go makes: janitor.New builds the
	// dynamic and discovery clients from restCfg itself.
	j, err := New(clientset, restCfg, Config{})
	require.NoError(t, err)
	require.NotNil(t, j.DynamicClient)
	require.NotNil(t, j.DiscoveryClient)

	// Exercise the typed clientset with a real List call (same shape the
	// janitor uses when listing namespaced resources).
	list, err := j.Clientset.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, "smoke-pod", list.Items[0].Name)

	// Exercise the discovery client New() constructs, hitting /version.
	info, err := j.DiscoveryClient.ServerVersion()
	require.NoError(t, err)
	require.Equal(t, "v1.31.0-fake", info.GitVersion)

	require.True(t, sawHTTP2.Load(), "fake API server never saw an HTTP/2 request; the client-go transport this repo relies on (golang.org/x/net/http2) was not exercised")
}
