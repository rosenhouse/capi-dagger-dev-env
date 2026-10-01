package infra

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// upstream serves one connection with serve and returns its address.
func upstream(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serve(conn)
	}()
	return l.Addr().String()
}

func proxyTo(t *testing.T, target string, limit int) net.Conn {
	t.Helper()
	addr, closeProxy, err := drainingProxy(target, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeProxy() })
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestDrainingProxyForwardsBothWays(t *testing.T) {
	client := proxyTo(t, upstream(t, func(c net.Conn) { io.Copy(c, c) }), 1<<20)
	client.SetDeadline(time.Now().Add(5 * time.Second))

	client.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(client, got); err != nil || string(got) != "ping" {
		t.Errorf("read %q, %v", got, err)
	}
}

func TestDrainingProxyReadsUpstreamWhileTheClientDoesNot(t *testing.T) {
	sent := make(chan error, 1)
	client := proxyTo(t, upstream(t, func(c net.Conn) {
		_, err := c.Write(make([]byte, 32<<20))
		sent <- err
	}), 64<<20)

	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream could not send while the client was not reading")
	}
	client.SetDeadline(time.Now().Add(5 * time.Second))
	n, err := io.Copy(io.Discard, io.LimitReader(client, 32<<20))
	if err != nil || n != 32<<20 {
		t.Errorf("client read %d bytes, %v", n, err)
	}
}

func TestDrainingProxyClosesAConnectionWhoseClientFallsTooFarBehind(t *testing.T) {
	sent := make(chan error, 1)
	proxyTo(t, upstream(t, func(c net.Conn) {
		_, err := c.Write(make([]byte, 32<<20))
		sent <- err
	}), 1<<20)

	select {
	case err := <-sent:
		if err == nil {
			t.Error("upstream sent everything to a client that read nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream blocked instead of the proxy closing the connection")
	}
}

func TestDrainingProxyClosesUpstreamWhenTheClientCloses(t *testing.T) {
	closed := make(chan struct{})
	client := proxyTo(t, upstream(t, func(c net.Conn) {
		io.Copy(io.Discard, c)
		close(closed)
	}), 1<<20)

	client.Close()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stayed open")
	}
}

func TestDrainingProxyForwardsEverythingBeforeUpstreamCloses(t *testing.T) {
	want := bytes.Repeat([]byte("x"), 1<<20)
	client := proxyTo(t, upstream(t, func(c net.Conn) { c.Write(want) }), 4<<20)
	client.SetDeadline(time.Now().Add(5 * time.Second))

	got, err := io.ReadAll(client)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("read %d bytes, %v", len(got), err)
	}
}

func TestClosingTheDrainingProxyClosesItsConnections(t *testing.T) {
	connected := make(chan struct{})
	addr, closeProxy, err := drainingProxy(upstream(t, func(c net.Conn) {
		close(connected)
		io.Copy(io.Discard, c)
	}), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-connected

	closeProxy()

	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read after close: %v", err)
	}
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Error("the proxy still accepts connections")
	}
}
