package devenv

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func TestStatusListsEnvironmentsTheirPortsAndOtherDevenvVMs(t *testing.T) {
	f := fakeSmolvm(t, func(alpha state.Env) []smolvm.Machine {
		return []smolvm.Machine{
			{Name: alpha.VM(), State: smolvm.Running},
			{Name: "devenv-sub-0bee3cf2", State: smolvm.Running},
			{Name: "unrelated", State: smolvm.Running},
		}
	})
	writeLastRun(t, f.env)
	if err := f.env.WritePorts(state.Ports{MgmtAPI: 40001, WorkloadAPI: 40002, Registry: 40003}); err != nil {
		t.Fatal(err)
	}
	beta, err := state.New(f.o.StateDir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(beta.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	if err := Status(t.Context(), f.o, &out); err != nil {
		t.Fatal(err)
	}

	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	kubeconfigs := filepath.Join(f.env.Dir, "mgmt.kubeconfig") + " " + filepath.Join(f.env.Dir, "workload.kubeconfig")
	want := []string{
		"NAME   VM       MGMT API  WORKLOAD API  REGISTRY  KUBECONFIGS",
		"alpha  running  40001     40002         40003     " + kubeconfigs,
		"beta   none",
		"",
		"Other devenv VMs, from other state dirs or deleted ones. Delete one with: smolvm machine delete -f --name <VM>",
		"devenv-sub-0bee3cf2  running",
	}
	if len(got) != len(want) {
		t.Fatalf("status =\n%s", out.String())
	}
	for i := range want {
		if strings.TrimRight(got[i], " ") != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSourceIsDevenvsOwnModule(t *testing.T) {
	root, err := Source(".")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.Abs(filepath.Join("..", "..")); root != want {
		t.Errorf("Source(.) = %q, want %q", root, want)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{other, filepath.Dir(other)} {
		if _, err := Source(dir); !errors.Is(err, errNoSource) {
			t.Errorf("Source(%s): err = %v", dir, err)
		}
	}
}

func TestTestOfARandomNameHintsHowToCleanUp(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
	f.o.Name = ""

	err := Test(t.Context(), f.o, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "\nclean up: go run ./cmd/devenv down --purge --name env-") {
		t.Errorf("err = %v", err)
	}
}
