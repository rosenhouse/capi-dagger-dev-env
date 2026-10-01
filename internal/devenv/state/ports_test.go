package state

import (
	"net"
	"testing"
)

func TestTakenOnIPv6LoopbackSeesAnotherListener(t *testing.T) {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	if !takenOnIPv6Loopback(port) {
		t.Errorf("port %d is free while another listener holds it", port)
	}
	l.Close()
	if takenOnIPv6Loopback(port) {
		t.Errorf("port %d is taken after its listener closed", port)
	}
}
