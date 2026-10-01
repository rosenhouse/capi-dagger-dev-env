package kube_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

func TestClientsTimeOutOnAServerThatNeverAnswers(t *testing.T) {
	kube.SetRequestTimeout(t, 50*time.Millisecond)
	path := hungServer(t)
	cs, err := kube.Client(path)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := kube.Dynamic(path)
	if err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"Client": func() error { return kube.NodesReady(context.Background(), cs) },
		"Dynamic": func() error {
			_, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "nodes"}).List(context.Background(), metav1.ListOptions{})
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: no error", name)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: request did not time out", name)
		}
	}
}

// hungServer returns the path of a kubeconfig for a server that never answers.
func hungServer(t *testing.T) string {
	t.Helper()
	hung := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hung:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hung) })
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: srv.URL}
	cfg.AuthInfos["c"] = &clientcmdapi.AuthInfo{}
	cfg.Contexts["c"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "c"}
	cfg.CurrentContext = "c"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	return path
}
