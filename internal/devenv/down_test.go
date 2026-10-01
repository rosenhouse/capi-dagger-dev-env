package devenv

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// TestHelperHoldsLock stands in for "devenv up": it holds an environment's lock until SIGTERM.
func TestHelperHoldsLock(t *testing.T) {
	root := os.Getenv("DEVENV_TEST_HOLD_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	env, _ := state.New(root, "alpha")
	if _, err := env.Lock(); err != nil {
		os.Exit(2)
	}
	os.WriteFile(root+"/ready", nil, 0o600)
	<-ctx.Done()
}

func TestStopSignalsTheHolderAndWaitsForIt(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsLock$")
	cmd.Env = append(os.Environ(), "DEVENV_TEST_HOLD_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(root + "/ready"); err == nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("helper never took the lock")
		}
	}
	env, _ := state.New(root, "alpha")

	if err := stop(context.Background(), env, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	if pid, _ := env.Holder(); pid != 0 {
		t.Errorf("pid %d still holds the lock", pid)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("helper exited with %v", err)
	}
}

func TestStopIsANoOpWhenNothingRuns(t *testing.T) {
	env, _ := state.New(t.TempDir(), "alpha")
	if err := stop(context.Background(), env, time.Second); err != nil {
		t.Error(err)
	}
}
