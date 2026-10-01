package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
)

// The server stands in for an API server without WebSocket streaming.
func TestWebSocketRejectionTriggersTheSPDYFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "websockets not supported", http.StatusBadRequest)
	}))
	defer srv.Close()
	cfg := &rest.Config{Host: srv.URL}

	exec, err := remotecommand.NewWebSocketExecutor(cfg, "GET", srv.URL+"/exec")
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{}); !upgradeFailed(err) {
		t.Errorf("exec: upgradeFailed(%v) = false", err)
	}

	u, _ := url.Parse(srv.URL + "/portforward")
	dialer, err := portforward.NewSPDYOverWebsocketDialer(u, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name); !upgradeFailed(err) {
		t.Errorf("port-forward: upgradeFailed(%v) = false", err)
	}
}
