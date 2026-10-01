package infra

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"

	"dagger.io/dagger"
)

// drainLimit bounds what the host buffers for each tunnel connection whose client stops reading.
const drainLimit = 16 << 20

// tunnel forwards a host port to port of svc and returns the host port, and a function that stops forwarding.
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
			client, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
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

const chunkSize = 32 << 10

// drain forwards between client and upstream until either closes, then closes both.
func drain(client, upstream net.Conn, limit int) {
	go func() {
		io.Copy(upstream, client)
		client.Close()
		upstream.Close()
	}()
	chunks := make(chan []byte, max(1, limit/chunkSize))
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, chunkSize)
			n, err := upstream.Read(buf)
			if n > 0 {
				select {
				case chunks <- buf[:n]:
				default:
					client.Close()
					upstream.Close()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for chunk := range chunks {
		if _, err := client.Write(chunk); err != nil {
			upstream.Close()
			for range chunks {
			}
			return
		}
	}
}
