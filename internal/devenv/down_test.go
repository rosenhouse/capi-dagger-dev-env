package devenv

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func TestDownDeletesTheVMAndKubeconfigs(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	env := f.env
	writeLastRun(t, env)
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, false, &out); err != nil {
		t.Fatal(err)
	}

	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after down: %+v", got)
	}
	if got := f.calls(t); !slices.Contains(got, "machine delete --name "+env.VM()+" -f") {
		t.Errorf("smolvm calls = %q", got)
	}
	wantGone(t, env, "mgmt.kubeconfig", "workload.kubeconfig", "ports.json", "ready")
	if _, err := os.Stat(env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
	if out.String() != "Deleted the VM of environment alpha.\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestDownPurgesTheStateOfAnEnvironmentWithoutAVM(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	env := f.env
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, true, &out); err != nil {
		t.Fatal(err)
	}

	if got := f.calls(t); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, "delete") }) {
		t.Errorf("smolvm calls = %q", got)
	}
	wantGone(t, env, "")
	if want := "Environment alpha has no VM.\nDeleted " + env.Dir + ".\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestDownFindsANamedEnvironmentWhoseStateDirIsGone(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	if err := os.RemoveAll(f.o.StateDir); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, true, &out); err != nil {
		t.Fatal(err)
	}

	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after down: %+v", got)
	}
	if out.String() != "Deleted the VM of environment alpha.\n" {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(f.o.StateDir); !os.IsNotExist(err) {
		t.Errorf("state root: %v", err)
	}
}

func TestDownOfAnUnknownEnvironmentFailsAndCreatesNothing(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	f.o.Name = "typo"

	err := Down(t.Context(), f.o, false, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "no environment typo") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.o.StateDir, "typo")); !os.IsNotExist(err) {
		t.Errorf("state dir of typo: %v", err)
	}
	if got := f.machines(); len(got) != 1 {
		t.Errorf("machines = %+v", got)
	}
}

func TestDownWithoutANameDeletesTheOnlyRunningEnvironment(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	beta, err := state.New(f.o.StateDir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(beta.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.o.Name = ""
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, false, &out); err != nil {
		t.Fatal(err)
	}

	if out.String() != "Deleted the VM of environment alpha.\n" {
		t.Errorf("output = %q", out.String())
	}
	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after down: %+v", got)
	}
}

func TestDownChecksSmolvmsVersion(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	t.Setenv("FAKE_SMOLVM_VERSION", "smolvm 1.21.0")

	err := Down(t.Context(), f.o, true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "want smolvm "+smolvm.Version) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"--version"}) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(f.env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
}

func TestDownRefusesWhileAnotherCommandHoldsTheEnvironment(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	unlock, err := f.env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = Down(t.Context(), f.o, true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "another devenv command is using environment alpha") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(f.env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
}
