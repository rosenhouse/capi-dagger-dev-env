package devenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// TestHelperRunsAnEnvironment stands in for "devenv up" without bringing up any clusters.
func TestHelperRunsAnEnvironment(t *testing.T) {
	root := os.Getenv("DEVENV_TEST_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	e, err := start(context.Background(), Options{StateDir: root, Name: "alpha"})
	if err != nil {
		os.Exit(2)
	}
	os.WriteFile(root+"/ready", nil, 0o600)
	<-e.Context().Done()
	if err := e.Close(); err != nil {
		os.Exit(3)
	}
}

func TestDownStopsUpAndWaitsForIt(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunsAnEnvironment$")
	cmd.Env = append(os.Environ(), "DEVENV_TEST_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(root + "/ready"); err == nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("helper never started")
		}
	}
	env, _ := state.New(root, "alpha")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer

	if err := Down(ctx, env, &out); err != nil {
		t.Fatal(err)
	}

	if running, _ := env.Running(); running {
		t.Error("environment still running")
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("helper exited with %v", err)
	}
	if !strings.Contains(out.String(), "Stopping environment alpha.") {
		t.Errorf("output = %q", out.String())
	}
}

func TestDownOfAStoppedEnvironmentSaysSo(t *testing.T) {
	env, _ := state.New(t.TempDir(), "alpha")
	var out bytes.Buffer
	if err := Down(context.Background(), env, &out); err != nil || out.String() != "Environment alpha is not running.\n" {
		t.Errorf("Down() = %q, %v", out.String(), err)
	}
}

func TestPurgeRefusesARunningEnvironment(t *testing.T) {
	env, _ := state.New(t.TempDir(), "alpha")
	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := Purge(context.Background(), env); err == nil || !strings.Contains(err.Error(), "alpha is already running") {
		t.Errorf("err = %v", err)
	}
}

func TestStartForgetsThePreviousRunsKubeconfigs(t *testing.T) {
	root := t.TempDir()
	env, _ := state.New(root, "alpha")
	stale := filepath.Join(env.Dir, "workload.kubeconfig")
	if err := os.MkdirAll(env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	e, err := start(context.Background(), Options{StateDir: root, Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale kubeconfig: %v", err)
	}
}

func TestCloseStopsTheControlSocket(t *testing.T) {
	e, err := start(context.Background(), Options{StateDir: t.TempDir(), Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(e.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket after Close: %v", err)
	}
}

// TestMain keeps the control sockets of test environments, and of their helper processes, out of the real cache directory.
func TestMain(m *testing.M) {
	home := os.Getenv("DEVENV_TEST_HOME")
	owner := home == ""
	if owner {
		var err error
		if home, err = os.MkdirTemp("", "devenv-test-home"); err != nil {
			panic(err)
		}
		os.Setenv("DEVENV_TEST_HOME", home)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CACHE_HOME", "")
	code := m.Run()
	if owner {
		os.RemoveAll(home)
	}
	os.Exit(code)
}
