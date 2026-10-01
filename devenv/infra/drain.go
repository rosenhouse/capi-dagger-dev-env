package infra

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"dagger.io/dagger"
)

// drainLimit bounds what the host buffers for each tunnel connection whose client stops reading.
const drainLimit = 16 << 20

// tunnel forwards a host port to port of svc. It returns the host port and a function that stops forwarding.
// A Dagger host tunnel stalls all its connections while any one leaves data unread, so a proxy drains each one.
func tunnel(ctx context.Context, c *dagger.Client, svc *dagger.Service, port int) (int, func() error, error) {
	t, err := c.Host().Tunnel(svc, dagger.HostTunnelOpts{Ports: []dagger.PortForward{{Backend: port}}}).Start(ctx)
	if err != nil {
		return 0, nil, err
	}
	ports, err := t.Ports(ctx)
	if err != nil {
		return 0, nil, err
	}
	tunnelPort, err := ports[0].Port(ctx)
	if err != nil {
		return 0, nil, err
	}
	addr, closeProxy, err := drainingProxy("127.0.0.1:"+strconv.Itoa(tunnelPort), drainLimit)
	if err != nil {
		return 0, nil, err
	}
	_, p, _ := net.SplitHostPort(addr)
	hostPort, err := strconv.Atoi(p)
	return hostPort, closeProxy, err
}

// drainingProxy forwards connections on a local port to target, and reads from target as fast as it sends.
// It buffers at most limit bytes for a client that falls behind, then closes that client's connection.
func drainingProxy(target string, limit int) (addr string, closeAll func() error, err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	var mu sync.Mutex
	conns := map[net.Conn]bool{}
	closed := false
	track := func(cs ...net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return false
		}
		for _, c := range cs {
			conns[c] = true
		}
		return true
	}
	untrack := func(cs ...net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cs {
			delete(conns, c)
		}
	}
	go func() {
		for {
			conn, err := l.Accept()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			go func() {
				client := conn.(*net.TCPConn)
				defer client.Close()
				conn, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				upstream := conn.(*net.TCPConn)
				defer upstream.Close()
				if !track(client, upstream) {
					return
				}
				defer untrack(client, upstream)
				drain(client, upstream, limit)
			}()
		}
	}()
	return l.Addr().String(), func() error {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		for c := range conns {
			c.Close()
		}
		return l.Close()
	}, nil
}

// drain forwards between client and upstream until both directions end, then closes both.
// An error, or upstream getting more than limit bytes ahead of the client, closes both at once.
func drain(client, upstream *net.TCPConn, limit int) {
	defer client.Close()
	defer upstream.Close()
	abort := func() {
		client.Close()
		upstream.Close()
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := io.Copy(upstream, client); err != nil {
			abort()
			return
		}
		upstream.CloseWrite()
	})
	var (
		mu      sync.Mutex
		ready   = sync.NewCond(&mu)
		pending bytes.Buffer
		eof     bool
	)
	wg.Go(func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := upstream.Read(buf)
			mu.Lock()
			pending.Write(buf[:n])
			overflow := pending.Len() > limit
			eof = err != nil || overflow
			ready.Signal()
			mu.Unlock()
			if overflow {
				abort()
			}
			if eof {
				return
			}
		}
	})
	for {
		mu.Lock()
		for pending.Len() == 0 && !eof {
			ready.Wait()
		}
		if pending.Len() == 0 {
			mu.Unlock()
			client.CloseWrite()
			break
		}
		chunk := bytes.Clone(pending.Next(32 << 10))
		mu.Unlock()
		if _, err := client.Write(chunk); err != nil {
			abort()
			break
		}
	}
	wg.Wait()
}
