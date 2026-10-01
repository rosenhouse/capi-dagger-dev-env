package e2e

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

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

func TestClosingProxyClosesItsConnectionsWhenStopped(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := upstream.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	addr, stop, err := closingProxy(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()
	if _, err := client.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(make([]byte, 1)); err != nil {
		t.Fatalf("proxy did not forward: %v", err)
	}

	stop()

	for name, c := range map[string]net.Conn{"client": client, "upstream": server} {
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Errorf("%s side after stop: err = %v, want EOF", name, err)
		}
	}
}

func TestClosingProxyPassesOnAClose(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := upstream.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	addr, stop, err := closingProxy(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	defer server.Close()

	client.Close()

	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("upstream after the client closed: err = %v, want EOF", err)
	}
}
