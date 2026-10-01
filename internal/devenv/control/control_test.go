package control_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/control"
)

func TestRequestStreamsProgressAndSucceeds(t *testing.T) {
	path, _ := serve(t, map[string]control.Handler{"build": func(_ context.Context, _ []string, progress io.Writer) error {
		fmt.Fprintln(progress, "compiling")
		fmt.Fprint(progress, "linking")
		return nil
	}})
	var out bytes.Buffer

	if err := control.Request(context.Background(), path, "build", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "compiling\nlinking\n" {
		t.Errorf("progress = %q", out.String())
	}
}

func TestRequestReturnsTheHandlersError(t *testing.T) {
	path, _ := serve(t, map[string]control.Handler{"build": func(context.Context, []string, io.Writer) error {
		return errors.New("gate timed out\nlogs: /x")
	}})

	err := control.Request(context.Background(), path, "build", io.Discard)

	if err == nil || err.Error() != "gate timed out\nlogs: /x" {
		t.Errorf("err = %v", err)
	}
}

func TestRequestRunsTheNamedCommand(t *testing.T) {
	path, _ := serve(t, map[string]control.Handler{
		"a": func(_ context.Context, args []string, w io.Writer) error { fmt.Fprintln(w, "ran a", args); return nil },
		"b": func(_ context.Context, args []string, w io.Writer) error { fmt.Fprintln(w, "ran b", args); return nil },
	})
	var out bytes.Buffer

	if err := control.Request(context.Background(), path, "b x y", &out); err != nil || out.String() != "ran b [x y]\n" {
		t.Errorf("Request(b x y) = %q, %v", out.String(), err)
	}
	if err := control.Request(context.Background(), path, "c", io.Discard); err == nil || !strings.Contains(err.Error(), `unknown command "c"`) {
		t.Errorf("Request(c): err = %v", err)
	}
}

func TestRequestsRunConcurrently(t *testing.T) {
	waiting, release := make(chan struct{}), make(chan struct{})
	path, _ := serve(t, map[string]control.Handler{
		"wait":    func(context.Context, []string, io.Writer) error { close(waiting); <-release; return nil },
		"release": func(context.Context, []string, io.Writer) error { close(release); return nil },
	})
	waited := make(chan error)
	go func() { waited <- control.Request(context.Background(), path, "wait", io.Discard) }()
	<-waiting
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := control.Request(ctx, path, "release", io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("wait never finished")
	}
}

func TestServeFinishesARequestThatStopsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var finished atomic.Bool
	path, served := serveContext(t, ctx, map[string]control.Handler{"stop": func(context.Context, []string, io.Writer) error {
		cancel()
		time.Sleep(100 * time.Millisecond)
		finished.Store(true)
		return nil
	}})
	replied := make(chan error)
	go func() { replied <- control.Request(context.Background(), path, "stop", io.Discard) }()

	if err := <-served; err != nil {
		t.Errorf("Serve() = %v", err)
	}
	if !finished.Load() {
		t.Error("Serve returned before its request finished")
	}
	if err := <-replied; err != nil {
		t.Error(err)
	}
}

func TestRequestStopsWithItsContextAndCancelsTheHandler(t *testing.T) {
	handlerDone := make(chan struct{})
	path, _ := serve(t, map[string]control.Handler{"hang": func(ctx context.Context, _ []string, _ io.Writer) error {
		<-ctx.Done()
		close(handlerDone)
		return nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := control.Request(ctx, path, "hang", io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Error("handler still running after the client left")
	}
}

func TestServeStopsDespiteASilentClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path, served := serveContext(t, ctx, nil)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-served:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("Serve still waiting for a client that sent nothing")
	}
}

func TestServeCreatesTheSocketDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening, served := make(chan struct{}), make(chan error, 1)
	go func() { served <- control.Serve(ctx, path, nil, func() { close(listening) }) }()
	select {
	case <-listening:
	case err := <-served:
		t.Fatal(err)
	}
}

func TestServeReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening, served := make(chan struct{}), make(chan error, 1)
	go func() { served <- control.Serve(ctx, path, nil, func() { close(listening) }) }()
	select {
	case <-listening:
	case err := <-served:
		t.Fatal(err)
	}
}

func TestRequestExplainsWhenNothingListens(t *testing.T) {
	err := control.Request(context.Background(), filepath.Join(t.TempDir(), "none.sock"), "build", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is up running?") {
		t.Errorf("err = %v", err)
	}
}

func serve(t *testing.T, handlers map[string]control.Handler) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return serveContext(t, ctx, handlers)
}

func serveContext(t *testing.T, ctx context.Context, handlers map[string]control.Handler) (string, <-chan error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.sock")
	listening, served := make(chan struct{}), make(chan error, 1)
	go func() { served <- control.Serve(ctx, path, handlers, func() { close(listening) }) }()
	select {
	case <-listening:
	case err := <-served:
		t.Fatal(err)
	}
	return path, served
}
