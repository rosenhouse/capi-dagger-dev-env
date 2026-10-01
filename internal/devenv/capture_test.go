package devenv

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
)

// serveMachineHealthChecks points e's management kubeconfig at a server that lists one MachineHealthCheck,
// which counts healthy of 1 machines healthy. It calls each with smolvm's calls so far.
func serveMachineHealthChecks(t *testing.T, f fake, e *Environment, healthy int, each func(calls []string)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/cluster.x-k8s.io/v1beta2/namespaces/default/machinehealthchecks" {
			http.NotFound(w, r)
			return
		}
		each(f.calls(t))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "MachineHealthCheckList", "items": [{
			"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "MachineHealthCheck", "metadata": {"name": "work-md-0", "namespace": "default"},
			"status": {"expectedMachines": 1, "currentHealthy": %d}}]}`, healthy)
	}))
	t.Cleanup(server.Close)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %q}}]
users: [{name: u, user: {}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
`, server.URL)
	if err := os.WriteFile(e.MgmtKubeconfig, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureZeroFillsTheGuestOnceMachinesAreHealthy(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	t.Setenv("FAKE_SMOLVM_EXEC_STDOUT", "2600\n")
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)
	checked := false
	serveMachineHealthChecks(t, f, e, 1, func(calls []string) {
		checked = true
		if len(calls) > 0 {
			t.Errorf("smolvm calls = %q before the check; want none", calls)
		}
	})
	file := filepath.Join(t.TempDir(), "x.checkpoint")

	if err := e.capture(t.Context(), file); err != nil {
		t.Fatal(err)
	}

	got := f.calls(t)
	checkpoint := slices.Index(got, "machine checkpoint --name "+f.env.VM()+" -o "+file)
	if !checked || checkpoint != len(got)-1 {
		t.Errorf("smolvm calls = %q; want a check of the machines first, and the checkpoint last", got)
	}
	if !slices.ContainsFunc(got[:checkpoint], func(c string) bool { return strings.Contains(c, "mount -t tmpfs") }) {
		t.Errorf("smolvm calls = %q; want a zero fill before the checkpoint", got)
	}
	if want := "] zero-filled 512 MiB of guest memory\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestCaptureWaitsWhileAMachineIsUnhealthy(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	e := f.open(t)
	serveMachineHealthChecks(t, f, e, 0, func([]string) {})
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := e.capture(ctx, filepath.Join(t.TempDir(), "x.checkpoint"))

	if want := "counts 0 of 1 machines healthy"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v; want it to contain %q", err, want)
	}
	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q; want none", got)
	}
}

func TestCaptureSaysNothingOfAZeroFillThatFailed(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	t.Setenv("FAKE_SMOLVM_EXEC_STDOUT", "2600\n")
	e := f.open(t)
	serveMachineHealthChecks(t, f, e, 1, func([]string) {})
	if err := e.capture(t.Context(), filepath.Join(t.TempDir(), "x.checkpoint")); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(t)
	fill := slices.IndexFunc(calls, func(c string) bool { return strings.HasSuffix(c, " -c mkdir -p /mnt/zero") })
	if fill < 0 {
		t.Fatalf("smolvm calls = %q; want a zero fill", calls)
	}
	first := f.env.VM()
	f = fakeSmolvm(t, vm(smolvm.Running))
	t.Setenv("FAKE_SMOLVM_FAIL", strings.Replace(calls[fill], first, f.env.VM(), 1))
	var progress bytes.Buffer
	f.o.Progress = &progress
	e = f.open(t)
	serveMachineHealthChecks(t, f, e, 1, func([]string) {})

	err := e.capture(t.Context(), filepath.Join(t.TempDir(), "x.checkpoint"))

	if err == nil || strings.Contains(progress.String(), "zero-filled") {
		t.Errorf("err = %v, progress %q; want an error, and no zero fill reported", err, progress.String())
	}
}

func TestMemAvailableMiB(t *testing.T) {
	for meminfo, want := range map[string]int{
		"MemTotal:       16374652 kB\nMemFree:         1041804 kB\nMemAvailable:   12582912 kB\n": 12288,
		"MemTotal:       16374652 kB\n": 0,
		"MemAvailable:   lots kB\n":     0,
	} {
		if got := memAvailableMiB(meminfo); got != want {
			t.Errorf("memAvailableMiB(%q) = %d; want %d", meminfo, got, want)
		}
	}
}
