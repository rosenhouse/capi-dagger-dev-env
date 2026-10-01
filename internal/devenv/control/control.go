// Package control lets one devenv process ask an environment's up process to act, over a Unix socket.
//
// The client sends a command line of space-separated words. The server streams progress lines
// prefixed with "| ", then ends with "OK" or "ERROR" and the quoted error.
package control

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler acts on one request, writing progress as lines. Its ctx ends when the client leaves.
type Handler func(ctx context.Context, args []string, progress io.Writer) error

// Serve handles requests concurrently until ctx is done, then waits for the ones in flight.
// It calls listening once the socket accepts connections.
func Serve(ctx context.Context, path string, handlers map[string]Handler, listening func()) error {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	listening()
	var inFlight sync.WaitGroup
	defer inFlight.Wait()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		inFlight.Go(func() {
			defer conn.Close()
			handle(ctx, conn, handlers)
		})
	}
}

func handle(ctx context.Context, conn net.Conn, handlers map[string]Handler) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	command, args, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
	progress := &prefixWriter{w: conn}
	if h, ok := handlers[command]; ok {
		err = h(ctx, strings.Fields(args), progress)
	} else {
		err = fmt.Errorf("unknown command %q", command)
	}
	progress.flush()
	result := "OK"
	if err != nil {
		result = "ERROR " + strconv.Quote(err.Error())
	}
	fmt.Fprintln(conn, result)
}

// Request sends command to the server at path, copies its progress to out, and returns its error.
func Request(ctx context.Context, path, command string, out io.Writer) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("%w; is up running?", err)
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	if _, err := fmt.Fprintln(conn, command); err != nil {
		return err
	}
	lines := bufio.NewScanner(conn)
	for lines.Scan() {
		line := lines.Text()
		switch {
		case strings.HasPrefix(line, "| "):
			fmt.Fprintln(out, strings.TrimPrefix(line, "| "))
		case line == "OK":
			return nil
		case strings.HasPrefix(line, "ERROR "):
			msg, err := strconv.Unquote(strings.TrimPrefix(line, "ERROR "))
			if err != nil {
				return fmt.Errorf("malformed reply %q", line)
			}
			return errors.New(msg)
		}
	}
	if err := lines.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("up closed the connection without a result")
}

type prefixWriter struct {
	w       io.Writer
	partial []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexByte(p.partial, '\n')
		if i < 0 {
			return len(b), nil
		}
		if _, err := fmt.Fprintf(p.w, "| %s\n", p.partial[:i]); err != nil {
			return 0, err
		}
		p.partial = p.partial[i+1:]
	}
}

func (p *prefixWriter) flush() {
	if len(p.partial) > 0 {
		fmt.Fprintf(p.w, "| %s\n", p.partial)
		p.partial = nil
	}
}
