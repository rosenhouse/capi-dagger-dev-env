package smolvm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
)

// TestMain lets the test binary stand in for smolvm: with FAKE_SMOLVM_CALL set, it records its
// arguments, stdin and process group there and replies with FAKE_SMOLVM_STDOUT, FAKE_SMOLVM_STDERR and FAKE_SMOLVM_EXIT.
func TestMain(m *testing.M) {
	if path := os.Getenv("FAKE_SMOLVM_CALL"); path != "" {
		onInterrupt := os.Getenv("FAKE_SMOLVM_ON_INTERRUPT")
		interrupted := make(chan os.Signal, 1)
		switch onInterrupt {
		case "exit", "finish":
			signal.Notify(interrupted, os.Interrupt)
		case "ignore":
			signal.Ignore(os.Interrupt)
		}
		stdin, _ := io.ReadAll(os.Stdin)
		call, _ := json.Marshal(fakeCall{Args: os.Args[1:], Stdin: string(stdin), Pgid: syscall.Getpgrp(), IdleReclaim: os.Getenv("SMOLVM_IDLE_RECLAIM")})
		if err := os.WriteFile(path, call, 0o644); err != nil {
			panic(err)
		}
		fmt.Print(os.Getenv("FAKE_SMOLVM_STDOUT"))
		fmt.Fprint(os.Stderr, os.Getenv("FAKE_SMOLVM_STDERR"))
		switch onInterrupt {
		case "exit":
			<-interrupted
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(130)
		case "ignore":
			time.Sleep(time.Hour)
		case "finish":
			outcome := "finished"
			select {
			case <-interrupted:
				outcome = "interrupted"
			case <-time.After(300 * time.Millisecond):
			}
			if err := os.WriteFile(path+".outcome", []byte(outcome), 0o644); err != nil {
				panic(err)
			}
		}
		code, _ := strconv.Atoi(os.Getenv("FAKE_SMOLVM_EXIT"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type fakeCall struct {
	Args  []string
	Stdin string
	Pgid  int
	// IdleReclaim is smolvm's SMOLVM_IDLE_RECLAIM.
	IdleReclaim string
}

type fake struct {
	stdout, stderr string
	exit           int
	// onInterrupt makes the fake wait after replying: until SIGINT for "exit", or forever for "ignore".
	// For "finish", it waits a moment unless SIGINT comes first, and records which happened in <call>.outcome.
	onInterrupt string
}

// start returns a CLI that runs the fake, and a function that returns the fake's last call.
func (f fake) start(t *testing.T) (smolvm.CLI, func() fakeCall) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "call.json")
	t.Setenv("FAKE_SMOLVM_CALL", path)
	t.Setenv("FAKE_SMOLVM_STDOUT", f.stdout)
	t.Setenv("FAKE_SMOLVM_STDERR", f.stderr)
	t.Setenv("FAKE_SMOLVM_EXIT", strconv.Itoa(f.exit))
	t.Setenv("FAKE_SMOLVM_ON_INTERRUPT", f.onInterrupt)
	return smolvm.CLI{Path: os.Args[0]}, func() fakeCall {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var call fakeCall
		if err := json.Unmarshal(data, &call); err != nil {
			t.Fatal(err)
		}
		return call
	}
}

func TestCheckVersionAcceptsThePinnedVersion(t *testing.T) {
	c, call := fake{stdout: "smolvm 1.22.0\n"}.start(t)

	if err := c.CheckVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := call().Args; !reflect.DeepEqual(got, []string{"--version"}) {
		t.Errorf("args = %q", got)
	}
}

func TestCheckVersionRejectsOtherOutput(t *testing.T) {
	for _, out := range []string{"smolvm 1.21.0\n", "smolvm 1.22.0-rc.1\n", "smolvm\n"} {
		t.Run(out, func(t *testing.T) {
			c, _ := fake{stdout: out}.start(t)

			wantInstallHint(t, c.CheckVersion(t.Context()), fmt.Sprintf("%q", out))
		})
	}
}

func TestCheckVersionExplainsHowToInstallAMissingSmolvm(t *testing.T) {
	c := smolvm.CLI{Path: filepath.Join(t.TempDir(), "smolvm")}

	wantInstallHint(t, c.CheckVersion(t.Context()), "no such file")
}

func wantInstallHint(t *testing.T, err error, cause string) {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{cause, "smolvm 1.22.0", "scripts/install.sh | bash -s -- --version 1.22.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestCommandLines(t *testing.T) {
	ports := []smolvm.Port{{Host: 18080, Guest: 8080}, {Host: 16443, Guest: 6443}}
	for _, tc := range []struct {
		name string
		run  func(context.Context, smolvm.CLI) error
		want []string
	}{{
		"create",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Create(ctx, "m", smolvm.MachineConfig{CPUs: 2, MemoryMiB: 512, StorageGiB: 3, OverlayGiB: 1, Ports: ports})
		},
		[]string{"machine", "create", "--name", "m", "--net", "--net-backend", "virtio-net",
			"--cpus", "2", "--mem", "512", "--storage", "3", "--overlay", "1", "-p", "18080:8080", "-p", "16443:6443"},
	}, {
		"create with defaults",
		func(ctx context.Context, c smolvm.CLI) error { return c.Create(ctx, "m", smolvm.MachineConfig{}) },
		[]string{"machine", "create", "--name", "m", "--net", "--net-backend", "virtio-net"},
	}, {
		"create from checkpoint",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.CreateFromCheckpoint(ctx, "m", "/c/m.checkpoint")
		},
		[]string{"machine", "create", "--name", "m", "--from", "/c/m.checkpoint"},
	}, {
		"rebind ports",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.RebindPorts(ctx, "m", ports, []smolvm.Port{{Host: 28080, Guest: 8080}})
		},
		[]string{"machine", "update", "--name", "m", "--remove-port", "18080:8080", "--remove-port", "16443:6443", "-p", "28080:8080"},
	}, {
		"start",
		func(ctx context.Context, c smolvm.CLI) error { return c.Start(ctx, "m", smolvm.StartOptions{}) },
		[]string{"machine", "start", "--name", "m", "--proxy", ""},
	}, {
		"start branchable",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Start(ctx, "m", smolvm.StartOptions{Branchable: true})
		},
		[]string{"machine", "start", "--name", "m", "--proxy", "", "--branchable"},
	}, {
		"stop",
		func(ctx context.Context, c smolvm.CLI) error { return c.Stop(ctx, "m") },
		[]string{"machine", "stop", "--name", "m"},
	}, {
		"delete",
		func(ctx context.Context, c smolvm.CLI) error { return c.Delete(ctx, "m", smolvm.DeleteOptions{}) },
		[]string{"machine", "delete", "--name", "m", "-f"},
	}, {
		"delete cascade",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Delete(ctx, "m", smolvm.DeleteOptions{Cascade: true})
		},
		[]string{"machine", "delete", "--name", "m", "-f", "--cascade"},
	}, {
		"branch",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Branch(ctx, "src", "m", smolvm.BranchOptions{})
		},
		[]string{"machine", "branch", "--from", "src", "--name", "m"},
	}, {
		"branch frozen with ports",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Branch(ctx, "src", "m", smolvm.BranchOptions{FreezeSource: true, Ports: ports[:1]})
		},
		[]string{"machine", "branch", "--from", "src", "--name", "m", "--freeze-source", "-p", "18080:8080"},
	}, {
		"checkpoint",
		func(ctx context.Context, c smolvm.CLI) error { return c.Checkpoint(ctx, "m", "/c/m.checkpoint") },
		[]string{"machine", "checkpoint", "--name", "m", "-o", "/c/m.checkpoint"},
	}, {
		"exec",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Exec(ctx, "m", []string{"echo", "a b"}, smolvm.ExecOptions{Env: []string{"A=1", "B=2"}, Timeout: 1500 * time.Millisecond})
		},
		[]string{"machine", "exec", "--name", "m", "-e", "A=1", "-e", "B=2", "--timeout", "1500ms", "--stream", "--", "echo", "a b"},
	}, {
		"exec with a timeout under 1ms",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Exec(ctx, "m", []string{"true"}, smolvm.ExecOptions{Timeout: time.Nanosecond})
		},
		[]string{"machine", "exec", "--name", "m", "--timeout", "1ms", "--stream", "--", "true"},
	}, {
		"exec with a negative timeout",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Exec(ctx, "m", []string{"true"}, smolvm.ExecOptions{Timeout: -time.Second})
		},
		[]string{"machine", "exec", "--name", "m", "--stream", "--", "true"},
	}, {
		"exec with stdin",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.Exec(ctx, "m", []string{"cat"}, smolvm.ExecOptions{Stdin: strings.NewReader("")})
		},
		[]string{"machine", "exec", "--name", "m", "-i", "--", "cat"},
	}, {
		"run",
		func(ctx context.Context, c smolvm.CLI) error {
			_, err := c.Run(ctx, "m", "echo hi\nexit 0", smolvm.ExecOptions{Env: []string{"A=1"}})
			return err
		},
		[]string{"machine", "exec", "--name", "m", "-e", "A=1", "--stream", "--", "sh", "-euo", "pipefail", "-c", "echo hi\nexit 0"},
	}, {
		"copy in",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.CopyIn(ctx, "m", "/h/kind", "/usr/local/bin/kind", 0o755)
		},
		[]string{"machine", "cp", "--mode", "755", "/h/kind", "m:/usr/local/bin/kind"},
	}, {
		"copy out",
		func(ctx context.Context, c smolvm.CLI) error {
			return c.CopyOut(ctx, "m", "/tmp/logs.tgz", "/h/logs.tgz")
		},
		[]string{"machine", "cp", "m:/tmp/logs.tgz", "/h/logs.tgz"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			c, call := fake{}.start(t)

			if err := tc.run(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if got := call().Args; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// lsJSON is smolvm 1.22.0's `machine ls --json` output, trimmed to a few fields.
const lsJSON = `[
  {"branchable": true, "cpus": 1, "name": "src", "parent_machine": null, "pid": 41, "ports": 1, "state": "frozen"},
  {"branchable": false, "cpus": 1, "name": "child", "parent_machine": "src", "pid": 42, "ports": 1, "state": "running"}
]`

func TestListParsesMachines(t *testing.T) {
	c, call := fake{stdout: lsJSON}.start(t)

	got, err := c.List(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	want := []smolvm.Machine{{Name: "src", State: smolvm.Frozen}, {Name: "child", State: smolvm.Running, Parent: "src"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v; want %+v", got, want)
	}
	if args := call().Args; !reflect.DeepEqual(args, []string{"machine", "ls", "--json"}) {
		t.Errorf("args = %q", args)
	}
}

func TestStatusParsesTheMachine(t *testing.T) {
	c, call := fake{stdout: `{"name": "m", "parent_machine": null, "state": "created", "storage_gb": null}`}.start(t)

	got, err := c.Status(t.Context(), "m")

	if err != nil {
		t.Fatal(err)
	}
	if want := (smolvm.Machine{Name: "m", State: smolvm.Created}); got != want {
		t.Errorf("Status() = %+v; want %+v", got, want)
	}
	if args := call().Args; !reflect.DeepEqual(args, []string{"machine", "status", "--name", "m", "--json"}) {
		t.Errorf("args = %q", args)
	}
}

func TestStatusRejectsMalformedOutput(t *testing.T) {
	c, _ := fake{stdout: "created"}.start(t)

	if _, err := c.Status(t.Context(), "m"); err == nil {
		t.Error("no error")
	}
}

func TestDataDirTrimsTheNewline(t *testing.T) {
	c, call := fake{stdout: "/home/u/.cache/smolvm/vms/628b49d96dcde97a\n"}.start(t)

	got, err := c.DataDir(t.Context(), "m")

	if err != nil || got != "/home/u/.cache/smolvm/vms/628b49d96dcde97a" {
		t.Errorf("DataDir() = %q, %v", got, err)
	}
	if args := call().Args; !reflect.DeepEqual(args, []string{"machine", "data-dir", "--name", "m"}) {
		t.Errorf("args = %q", args)
	}
}

func TestCommandErrorsCarrySmolvmsStderr(t *testing.T) {
	c, _ := fake{stderr: "Error: vm not found: m\n", exit: 1}.start(t)

	err := c.Stop(t.Context(), "m")

	var exit *smolvm.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("error = %v; want an ExitError with code 1", err)
	}
	if want := "smolvm machine stop --name m: exit 1\nError: vm not found: m"; err.Error() != want {
		t.Errorf("error =\n%s\nwant\n%s", err, want)
	}
}

func TestExecForwardsStdinAndOutput(t *testing.T) {
	c, call := fake{stdout: "out", stderr: "err"}.start(t)
	var stdout, stderr strings.Builder

	err := c.Exec(t.Context(), "m", []string{"cat"},
		smolvm.ExecOptions{Stdin: strings.NewReader("in"), Stdout: &stdout, Stderr: &stderr})

	if err != nil {
		t.Fatal(err)
	}
	if got := call().Stdin; got != "in" {
		t.Errorf("stdin = %q", got)
	}
	if stdout.String() != "out" || stderr.String() != "err" {
		t.Errorf("stdout, stderr = %q, %q", stdout.String(), stderr.String())
	}
}

func TestExecReturnsTheExitCodeAndTheTailOfStderr(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	allStderr := strings.Join(lines, "\n") + "\n"
	c, _ := fake{stderr: allStderr, exit: 3}.start(t)
	var stderr strings.Builder

	err := c.Exec(t.Context(), "m", []string{"false"}, smolvm.ExecOptions{Stderr: &stderr})

	var exit *smolvm.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("error = %v; want an ExitError", err)
	}
	if exit.Code != 3 {
		t.Errorf("Code = %d", exit.Code)
	}
	if want := strings.Join(lines[10:], "\n"); exit.Stderr != want {
		t.Errorf("Stderr =\n%s\nwant\n%s", exit.Stderr, want)
	}
	if stderr.String() != allStderr {
		t.Errorf("caller's stderr = %q", stderr.String())
	}
}

func TestExecKeepsTheTailOfALongLine(t *testing.T) {
	long := strings.Repeat("x", 100_000) + "end"
	c, _ := fake{stderr: long, exit: 1}.start(t)

	err := c.Exec(t.Context(), "m", []string{"false"}, smolvm.ExecOptions{})

	var exit *smolvm.ExitError
	if !errors.As(err, &exit) || !strings.HasSuffix(exit.Stderr, "xend") || len(exit.Stderr) > 10_000 {
		t.Errorf("error = %.100v...", err)
	}
}

func TestExecErrorsNameTheCommandButHideEnvValues(t *testing.T) {
	c, _ := fake{exit: 1}.start(t)

	err := c.Exec(t.Context(), "m", []string{"docker", "load"}, smolvm.ExecOptions{Env: []string{"TOKEN=secret"}})

	if want := "smolvm machine exec --name m -e TOKEN --stream -- docker: exit 1"; err == nil || err.Error() != want {
		t.Errorf("error = %v; want %s", err, want)
	}
}

func TestExecWithoutACommandReturnsSmolvmsError(t *testing.T) {
	c, _ := fake{exit: 2}.start(t)

	err := c.Exec(t.Context(), "m", nil, smolvm.ExecOptions{})

	if want := "smolvm machine exec --name m --stream --: exit 2"; err == nil || err.Error() != want {
		t.Errorf("error = %v; want %s", err, want)
	}
}

func TestSpawnReturnsTheGuestPID(t *testing.T) {
	c, call := fake{stdout: "42\n"}.start(t)

	pid, err := c.Spawn(t.Context(), "m", []string{"sleep", "9"}, []string{"A=1"})

	if err != nil || pid != 42 {
		t.Errorf("Spawn() = %d, %v; want 42", pid, err)
	}
	if want := []string{"machine", "exec", "--name", "m", "-e", "A=1", "-d", "--", "sleep", "9"}; !reflect.DeepEqual(call().Args, want) {
		t.Errorf("args =\n%q\nwant\n%q", call().Args, want)
	}
}

func TestSpawnRejectsOutputWithoutAPID(t *testing.T) {
	c, _ := fake{stdout: "started\n"}.start(t)

	_, err := c.Spawn(t.Context(), "m", []string{"sleep", "9"}, nil)

	if want := `smolvm machine exec --name m -d -- sleep printed "started\n"; want a PID`; err == nil || err.Error() != want {
		t.Errorf("error = %v; want %s", err, want)
	}
}

func TestSpawnErrorsCarrySmolvmsStderr(t *testing.T) {
	c, _ := fake{stderr: "Error: not running\n", exit: 1}.start(t)

	_, err := c.Spawn(t.Context(), "m", []string{"sleep", "9"}, nil)

	if want := "smolvm machine exec --name m -d -- sleep: exit 1\nError: not running"; err == nil || err.Error() != want {
		t.Errorf("error = %v; want %s", err, want)
	}
}

func TestSmolvmRunsInItsOwnProcessGroup(t *testing.T) {
	c, call := fake{stdout: "smolvm 1.22.0\n"}.start(t)

	if err := c.CheckVersion(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := call().Pgid; got == syscall.Getpgrp() {
		t.Errorf("smolvm shares devenv's process group %d, so a terminal's Ctrl-C reaches it", got)
	}
}

func TestCancelledStartLetsSmolvmFinishTheStart(t *testing.T) {
	c, _ := fake{onInterrupt: "finish"}.start(t)
	path := os.Getenv("FAKE_SMOLVM_CALL")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- c.Start(ctx, "m", smolvm.StartOptions{}) }()
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if outcome, err := os.ReadFile(path + ".outcome"); string(outcome) != "finished" {
		t.Errorf("smolvm machine start %s, %v; want it finished", outcome, err)
	}
}

func TestStartCanTurnOffIdleReclaim(t *testing.T) {
	t.Setenv("SMOLVM_IDLE_RECLAIM", "")
	c, call := fake{}.start(t)

	if err := c.Start(t.Context(), "m", smolvm.StartOptions{NoIdleReclaim: true}); err != nil {
		t.Fatal(err)
	}

	if got := call().IdleReclaim; got != "off" {
		t.Errorf("SMOLVM_IDLE_RECLAIM = %q", got)
	}
}

func TestAgentTimedOut(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Error: agent operation failed: wait for ready: clone agent did not respond to ping within timeout (socket_exists=false)\n": true,
		"Error: host port 18080 is already in use\n": false,
	} {
		c, _ := fake{stderr: stderr, exit: 1}.start(t)

		if got := smolvm.AgentTimedOut(c.Start(t.Context(), "m", smolvm.StartOptions{})); got != want {
			t.Errorf("AgentTimedOut() = %v after %q", got, stderr)
		}
	}
	if smolvm.AgentTimedOut(errors.New("clone agent did not respond to ping within timeout")) {
		t.Error("AgentTimedOut() is true for an error that smolvm did not return")
	}
}

func TestErrorsQuoteEmptyArguments(t *testing.T) {
	c, _ := fake{exit: 1}.start(t)

	err := c.Start(t.Context(), "m", smolvm.StartOptions{})

	if want := `smolvm machine start --name m --proxy "": exit 1`; err == nil || err.Error() != want {
		t.Errorf("err = %v; want %s", err, want)
	}
}

func TestStartWithAnEndedContextRunsNothing(t *testing.T) {
	c, _ := fake{}.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := c.Start(ctx, "m", smolvm.StartOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if _, err := os.Stat(os.Getenv("FAKE_SMOLVM_CALL")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("smolvm ran: %v", err)
	}
}

func TestCancellingInterruptsSmolvm(t *testing.T) {
	c, _ := fake{stdout: "started", stderr: "starting\n", onInterrupt: "exit"}.start(t)
	ctx, cancel := context.WithCancel(t.Context())

	err := execUntilCancelled(t, c, ctx, cancel)

	if want := "smolvm machine exec --name m --stream -- sleep: context canceled\nstarting\ninterrupted"; !errors.Is(err, context.Canceled) || err.Error() != want {
		t.Errorf("error =\n%v\nwant context.Canceled and\n%s", err, want)
	}
}

func TestCancellingKillsSmolvmIfItIgnoresInterrupts(t *testing.T) {
	smolvm.SetWaitDelay(t, 10*time.Millisecond)
	c, _ := fake{stdout: "started", onInterrupt: "ignore"}.start(t)
	ctx, cancel := context.WithCancel(t.Context())

	err := execUntilCancelled(t, c, ctx, cancel)

	if want := "smolvm machine exec --name m --stream -- sleep: context canceled"; !errors.Is(err, context.Canceled) || err.Error() != want {
		t.Errorf("error = %v; want context.Canceled and %s", err, want)
	}
}

// execUntilCancelled cancels an Exec once the fake writes to stdout, and returns Exec's error.
func execUntilCancelled(t *testing.T, c smolvm.CLI, ctx context.Context, cancel context.CancelFunc) error {
	t.Helper()
	done := make(chan error)
	go func() {
		done <- c.Exec(ctx, "m", []string{"sleep", "9"}, smolvm.ExecOptions{Stdout: cancelOnWrite(cancel)})
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after its context was cancelled")
		return nil
	}
}

type cancelOnWrite context.CancelFunc

func (c cancelOnWrite) Write(p []byte) (int, error) {
	c()
	return len(p), nil
}

func TestRunReturnsStdoutAndCopiesTheOutput(t *testing.T) {
	c, call := fake{stdout: "hello\n", stderr: "noise\n"}.start(t)
	var stdout, stderr strings.Builder

	got, err := c.Run(t.Context(), "m", "cat", smolvm.ExecOptions{Stdin: strings.NewReader("in"), Stdout: &stdout, Stderr: &stderr})

	if err != nil || got != "hello\n" {
		t.Errorf("Run() = %q, %v", got, err)
	}
	if stdout.String() != "hello\n" || stderr.String() != "noise\n" {
		t.Errorf("stdout, stderr = %q, %q", stdout.String(), stderr.String())
	}
	if call().Stdin != "in" {
		t.Errorf("stdin = %q", call().Stdin)
	}
}

func TestRunErrorsNameTheScriptsFirstLine(t *testing.T) {
	c, _ := fake{stderr: "boom\n", exit: 2}.start(t)

	_, err := c.Run(t.Context(), "m", "\n  kind create cluster\n  kubectl get nodes\n", smolvm.ExecOptions{})

	var exit *smolvm.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("error = %v; want an ExitError with code 2", err)
	}
	if want := "kind create cluster: smolvm machine exec --name m --stream -- sh: exit 2\nboom"; err.Error() != want {
		t.Errorf("error =\n%s\nwant\n%s", err, want)
	}
}

func TestCopyRefusesHostPathsWithAColon(t *testing.T) {
	c, _ := fake{}.start(t)

	for _, err := range []error{
		c.CopyIn(t.Context(), "m", "/h/a:b", "/a", 0o644),
		c.CopyOut(t.Context(), "m", "/a", "/h/a:b"),
	} {
		if err == nil || !strings.Contains(err.Error(), "colon") {
			t.Errorf("err = %v", err)
		}
	}
}
