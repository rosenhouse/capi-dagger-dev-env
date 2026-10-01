package e2e

import (
	"context"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// fakeAPIServer answers pod lists and logs, rejects WebSocket exec, and handles SPDY exec with spdyExec.
// It counts its open connections.
func fakeAPIServer(t *testing.T, spdyExec http.HandlerFunc) (kubeconfig string, open *atomic.Int64) {
	t.Helper()
	open = &atomic.Int64{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pods") {
			w.Header().Set("Content-Type", "application/json")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods") && strings.Contains(r.URL.RawQuery, "kube-dns"):
			fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"coredns"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/pods"):
			fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"etcd"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/log"):
			fmt.Fprint(w, "log line")
		case strings.HasSuffix(r.URL.Path, "/exec") && r.Method == http.MethodGet:
			http.Error(w, "websockets not supported", http.StatusBadRequest)
		case strings.HasSuffix(r.URL.Path, "/exec"):
			spdyExec(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	config := clientcmdapi.NewConfig()
	config.Clusters["c"] = &clientcmdapi.Cluster{
		Server:                   srv.URL,
		CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}),
	}
	config.AuthInfos["c"] = &clientcmdapi.AuthInfo{}
	config.Contexts["c"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "c"}
	config.CurrentContext = "c"
	kubeconfig = filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*config, kubeconfig); err != nil {
		t.Fatal(err)
	}
	return kubeconfig, open
}

func TestAnAttemptEndsWhenAStepIgnoresItsContext(t *testing.T) {
	kubeconfig, _ := fakeAPIServer(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)

	go func() { done <- kubectlWorks(ctx, kubeconfig) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exec etcd") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attempt still running 10s after its context ended")
	}
}

func TestAnAttemptClosesItsConnections(t *testing.T) {
	kubeconfig, open := fakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no exec here", http.StatusInternalServerError)
	})

	if err := kubectlWorks(context.Background(), kubeconfig); err == nil || !strings.Contains(err.Error(), "exec etcd") {
		t.Fatalf("err = %v, want a failed exec", err)
	}

	for start := time.Now(); open.Load() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("%d connections still open after the attempt", open.Load())
		}
	}
}
