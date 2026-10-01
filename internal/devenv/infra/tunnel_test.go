package infra

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"dagger.io/dagger"
)

// TestHostTunnelServesWhileAConnectionLeavesDataUnread needs a Dagger engine, so it runs only with DEVENV_ENGINE_TESTS set.
func TestHostTunnelServesWhileAConnectionLeavesDataUnread(t *testing.T) {
	if os.Getenv("DEVENV_ENGINE_TESTS") == "" {
		t.Skip("set DEVENV_ENGINE_TESTS to run against a Dagger engine")
	}
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A client that sends "flood" gets endless data; any other line is echoed back.
	svc, err := c.Container().From(socatImage).
		With(InSession).
		WithExposedPort(7000).
		AsService(dagger.ContainerAsServiceOpts{Args: []string{"sh", "-c",
			`socat TCP-LISTEN:7000,fork,reuseaddr SYSTEM:'read l; if [ "$l" = flood ]; then yes; else echo "$l"; fi'`}}).
		Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := c.Host().Tunnel(svc, dagger.HostTunnelOpts{Ports: []dagger.PortForward{{Backend: 7000}}}).Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := tunnel.Ports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ports[0].Port(ctx)
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	echo := func() error {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintln(conn, "ping")
		_, err = io.ReadFull(conn, make([]byte, 5))
		return err
	}
	if err := echo(); err != nil {
		t.Fatalf("before: %v", err)
	}
	unread, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer unread.Close()
	fmt.Fprintln(unread, "flood")
	time.Sleep(10 * time.Second)

	if err := echo(); err != nil {
		t.Errorf("while another connection leaves data unread: %v", err)
	}
}
