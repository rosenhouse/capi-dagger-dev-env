package smolvm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
)

// The tests in this file boot microVMs, so they run only with DEVENV_SMOLVM_TESTS set.

func TestMachineLifecycle(t *testing.T) {
	c := requireSmolvm(t)
	ctx := t.Context()
	source, restored, branch := machineName(), machineName(), machineName()
	ports := freePorts(t, 3)
	const served = "served from RAM\n"
	// Start ignores a host proxy that the guest cannot reach.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	deleteLater(t, c, source)
	must(t, c.Create(ctx, source, smolvm.MachineConfig{CPUs: 1, MemoryMiB: 512, StorageGiB: 2, OverlayGiB: 1,
		Ports: []smolvm.Port{{Host: ports[0], Guest: 8080}}}))
	must(t, c.Start(ctx, source, smolvm.StartOptions{Branchable: true, NoIdleReclaim: true}))

	if out, err := c.Run(ctx, source, "echo hello\nuname -s", smolvm.ExecOptions{}); err != nil || out != "hello\nLinux\n" {
		t.Fatalf("Run() = %q, %v", out, err)
	}

	in := filepath.Join(t.TempDir(), "in")
	must(t, os.WriteFile(in, []byte("copied\n"), 0o600))
	must(t, c.CopyIn(ctx, source, in, "/opt/copy/a", 0o750))
	if out, err := c.Run(ctx, source, "stat -c %a /opt/copy/a; cat /opt/copy/a", smolvm.ExecOptions{}); err != nil || out != "750\ncopied\n" {
		t.Fatalf("after CopyIn, Run() = %q, %v", out, err)
	}
	out := filepath.Join(t.TempDir(), "out")
	must(t, c.CopyOut(ctx, source, "/opt/copy/a", out))
	if got, err := os.ReadFile(out); err != nil || string(got) != "copied\n" {
		t.Fatalf("CopyOut wrote %q, %v", got, err)
	}

	var stdout strings.Builder
	err := c.Exec(ctx, source, []string{"sh", "-c", `cat; echo "$GREETING" >&2; exit 3`},
		smolvm.ExecOptions{Env: []string{"GREETING=bye"}, Stdin: strings.NewReader("ping"), Stdout: &stdout})
	var exit *smolvm.ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || !strings.Contains(exit.Stderr, "bye") || stdout.String() != "ping" {
		t.Fatalf("Exec() wrote %q and returned %v; want ping and exit 3 with bye", stdout.String(), err)
	}

	err = c.Exec(ctx, source, []string{"sleep", "30"}, smolvm.ExecOptions{Timeout: time.Second})
	if !errors.As(err, &exit) || exit.Code != 124 {
		t.Fatalf("Exec() with a timeout returned %v; want exit 124", err)
	}

	if _, err := c.Run(ctx, source, "echo 'served from RAM' >/dev/shm/msg", smolvm.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	pid, err := c.Spawn(ctx, source, []string{"nc", "-lk", "-p", "8080", "-e", "cat", "/dev/shm/msg"}, nil)
	must(t, err)
	wantServed(t, ports[0], served)
	if comm, err := c.Run(ctx, source, fmt.Sprintf("cat /proc/%d/comm", pid), smolvm.ExecOptions{}); err != nil || comm != "nc\n" {
		t.Errorf("Spawn() returned PID %d, whose command is %q, %v; want nc", pid, comm, err)
	}

	file := filepath.Join(t.TempDir(), "source.checkpoint")
	must(t, c.Checkpoint(ctx, source, file))
	recorded, err := smolvm.CheckpointContract(file)
	must(t, err)
	host, err := smolvm.HostContract()
	must(t, err)
	if recorded != host {
		t.Errorf("checkpoint records contract %s; HostContract() = %s", recorded, host)
	}
	must(t, c.Delete(ctx, source, smolvm.DeleteOptions{}))

	deleteLater(t, c, restored)
	must(t, c.CreateFromCheckpoint(ctx, restored, file))
	wantState(t, c, restored, smolvm.Created)
	must(t, c.RebindPorts(ctx, restored, []smolvm.Port{{Host: ports[0], Guest: 8080}}, []smolvm.Port{{Host: ports[1], Guest: 8080}}))
	must(t, c.Start(ctx, restored, smolvm.StartOptions{NoIdleReclaim: true}))
	if out, err := c.Run(ctx, restored, "cat /dev/shm/msg", smolvm.ExecOptions{}); err != nil || out != served {
		t.Errorf("Run() in the restored machine = %q, %v", out, err)
	}
	wantServed(t, ports[1], served)

	deleteLater(t, c, branch)
	must(t, c.Branch(ctx, restored, branch, smolvm.BranchOptions{FreezeSource: true, Ports: []smolvm.Port{{Host: ports[2], Guest: 8080}}}))
	wantServed(t, ports[2], served)
	machines, err := c.List(ctx)
	must(t, err)
	for _, want := range []smolvm.Machine{{Name: restored, State: smolvm.Frozen}, {Name: branch, State: smolvm.Running, Parent: restored}} {
		if !slices.Contains(machines, want) {
			t.Errorf("List() = %+v; want it to contain %+v", machines, want)
		}
	}

	must(t, c.Stop(ctx, branch))
	wantState(t, c, branch, smolvm.Stopped)

	must(t, c.Delete(ctx, restored, smolvm.DeleteOptions{Cascade: true}))
	machines, err = c.List(ctx)
	must(t, err)
	for _, m := range machines {
		if m.Name == restored || m.Name == branch {
			t.Errorf("List() = %+v after deleting %s with its branches", machines, restored)
		}
	}
}

func TestMissingMachine(t *testing.T) {
	c := requireSmolvm(t)
	name := machineName()

	_, err := c.Status(t.Context(), name)

	var exit *smolvm.ExitError
	if !errors.As(err, &exit) || !strings.Contains(exit.Stderr, "not found") {
		t.Errorf("Status() of a missing machine returned %v; want smolvm's not found", err)
	}
}

func requireSmolvm(t *testing.T) smolvm.CLI {
	t.Helper()
	if os.Getenv("DEVENV_SMOLVM_TESTS") == "" {
		t.Skip("set DEVENV_SMOLVM_TESTS to boot smolvm machines")
	}
	var c smolvm.CLI
	must(t, c.CheckVersion(t.Context()))
	return c
}

func machineName() string { return fmt.Sprintf("devenv-test-%08x", rand.Uint32()) }

// deleteLater deletes the machine at the end of the test, if it still exists. If the test failed,
// it first keeps the machine's console log in $DEVENV_SMOLVM_LOGS, or logs it.
func deleteLater(t *testing.T, c smolvm.CLI, name string) {
	t.Cleanup(func() {
		ctx := context.Background()
		machines, err := c.List(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		if !slices.ContainsFunc(machines, func(m smolvm.Machine) bool { return m.Name == name }) {
			return
		}
		if t.Failed() {
			keepConsoleLog(t, c, name)
		}
		if err := c.Delete(ctx, name, smolvm.DeleteOptions{Cascade: true}); err != nil {
			t.Error(err)
		}
	})
}

func keepConsoleLog(t *testing.T, c smolvm.CLI, name string) {
	dir, err := c.DataDir(context.Background(), name)
	if err != nil {
		t.Log(err)
		return
	}
	log, err := os.ReadFile(filepath.Join(dir, "agent-console.log"))
	if err != nil {
		t.Log(err)
		return
	}
	logs := os.Getenv("DEVENV_SMOLVM_LOGS")
	if logs == "" {
		t.Logf("%s agent-console.log:\n%s", name, log)
		return
	}
	if err = os.MkdirAll(logs, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(logs, name+"-agent-console.log"), log, 0o644)
	}
	if err != nil {
		t.Log(err)
	}
}

func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		defer l.Close()
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports
}

// wantServed waits for the guest server behind a published port. smolvm accepts a connection
// even when nothing listens in the guest, so it compares what the server sends.
func wantServed(t *testing.T, port int, want string) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if got = probe(port); got == want {
			return
		}
	}
	t.Fatalf("127.0.0.1:%d served %q; want %q", port, got, want)
}

func probe(port int) string {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		return err.Error()
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err.Error()
	}
	got, _ := io.ReadAll(conn)
	return string(got)
}

func wantState(t *testing.T, c smolvm.CLI, name string, want smolvm.State) {
	t.Helper()
	m, err := c.Status(t.Context(), name)
	must(t, err)
	if m.State != want {
		t.Fatalf("%s is %s; want %s", name, m.State, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
