package state_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func TestNewRejectsNamesThatAreNotDNSLabels(t *testing.T) {
	if _, err := state.New(t.TempDir(), "Not_A_Label"); err == nil {
		t.Error("accepted invalid name")
	}
}

func TestNewRejectsNamesThatSmolvmCannotUse(t *testing.T) {
	if _, err := state.New(t.TempDir(), "a--b"); err == nil || !strings.Contains(err.Error(), "consecutive hyphens") {
		t.Errorf("err = %v", err)
	}
}

func TestNewGeneratesDistinctValidNames(t *testing.T) {
	a := newEnv(t, t.TempDir(), "")
	b := newEnv(t, t.TempDir(), "")
	if errs := validation.IsDNS1123Label(a.Name); len(errs) > 0 {
		t.Errorf("generated name %q: %v", a.Name, errs)
	}
	if a.Name == b.Name {
		t.Errorf("generated the same name twice: %q", a.Name)
	}
}

func TestDirIsAbsoluteAndKeyedByName(t *testing.T) {
	t.Chdir(t.TempDir())
	env := newEnv(t, ".devenv", "alpha")
	want, _ := filepath.Abs(filepath.Join(".devenv", "alpha"))
	if env.Dir != want {
		t.Errorf("dir = %q, want %q", env.Dir, want)
	}
}

func TestIDDistinguishesStateDirsThatShareAName(t *testing.T) {
	root := t.TempDir()
	a := newEnv(t, root, "alpha")
	if again := newEnv(t, root, "alpha"); again.ID != a.ID {
		t.Errorf("ID changed between calls: %q, %q", a.ID, again.ID)
	}
	if other := newEnv(t, t.TempDir(), "alpha"); other.ID == a.ID {
		t.Errorf("two state dirs share ID %q", a.ID)
	}
	if !strings.HasPrefix(a.ID, "alpha-") {
		t.Errorf("ID %q does not start with the name", a.ID)
	}
}

func TestASymlinkedRootNamesTheSameEnvironment(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	viaLink, direct := newEnv(t, filepath.Join(link, ".devenv"), "alpha"), newEnv(t, filepath.Join(real, ".devenv"), "alpha")

	if viaLink != direct {
		t.Errorf("through a symlink %+v, directly %+v", viaLink, direct)
	}
}

// smolvmName matches the machine names smolvm accepts: src/data/mod.rs validate_vm_name in smolvm 1.22.0.
var smolvmName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_]|-[A-Za-z0-9_])*$`)

func TestVMIsAValidSmolvmNameUniqueToTheStateDir(t *testing.T) {
	longest := strings.Repeat("a", 62) + "z"
	a, b := newEnv(t, t.TempDir(), longest), newEnv(t, t.TempDir(), longest)
	if !smolvmName.MatchString(a.VM()) || len(a.VM()) > 128 {
		t.Errorf("VM() = %q, which smolvm rejects", a.VM())
	}
	if a.VM() == b.VM() {
		t.Errorf("two state dirs share VM %q", a.VM())
	}
}

func TestLockRejectsASecondHolderUntilReleased(t *testing.T) {
	root := t.TempDir()
	unlock, err := newEnv(t, root, "alpha").Lock()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := newEnv(t, root, "alpha").Lock(); err == nil || !strings.Contains(err.Error(), "another devenv command is using environment alpha") {
		t.Errorf("second Lock: err = %v", err)
	}

	unlock()
	if _, err := newEnv(t, root, "alpha").Lock(); err != nil {
		t.Errorf("Lock after unlock: %v", err)
	}
}

func TestWaitLockWaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "start.lock")
	unlock, err := state.WaitLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	released := time.Now().Add(100 * time.Millisecond)
	time.AfterFunc(time.Until(released), unlock)

	unlockAgain, err := state.WaitLock(t.Context(), path)

	if err != nil {
		t.Fatal(err)
	}
	unlockAgain()
	if time.Now().Before(released) {
		t.Error("took the lock while another holder had it")
	}
}

func TestWaitLockGivesUpWhenTheContextEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.lock")
	unlock, err := state.WaitLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if _, err := state.WaitLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestWriteKubeconfigTargetsLocalhostPortUnderEnvName(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	in := clientcmdapi.NewConfig()
	in.Clusters["kind-mgmt"] = &clientcmdapi.Cluster{Server: "https://0.0.0.0:6443", TLSServerName: "docker", CertificateAuthorityData: []byte("ca")}
	in.AuthInfos["kind-mgmt"] = &clientcmdapi.AuthInfo{ClientCertificateData: []byte("cert")}
	in.Contexts["kind-mgmt"] = &clientcmdapi.Context{Cluster: "kind-mgmt", AuthInfo: "kind-mgmt"}
	in.CurrentContext = "kind-mgmt"
	raw, _ := clientcmd.Write(*in)

	path, err := env.WriteKubeconfig("mgmt", raw, 41234)
	if err != nil {
		t.Fatal(err)
	}

	if path != filepath.Join(env.Dir, "mgmt.kubeconfig") {
		t.Errorf("path = %q", path)
	}
	out, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.CurrentContext != "alpha-mgmt" {
		t.Errorf("current context = %q", out.CurrentContext)
	}
	ctx := out.Contexts["alpha-mgmt"]
	if ctx == nil || ctx.Cluster != "alpha-mgmt" || ctx.AuthInfo != "alpha-mgmt" {
		t.Fatalf("context = %+v", ctx)
	}
	cluster := out.Clusters["alpha-mgmt"]
	if cluster == nil || cluster.Server != "https://localhost:41234" || cluster.TLSServerName != "" || string(cluster.CertificateAuthorityData) != "ca" {
		t.Errorf("cluster = %+v", cluster)
	}
	if user := out.AuthInfos["alpha-mgmt"]; user == nil || string(user.ClientCertificateData) != "cert" {
		t.Error("lost client certificate")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func newEnv(t *testing.T, root, name string) state.Env {
	t.Helper()
	env, err := state.New(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestRunningMeansTheVMIsRunning(t *testing.T) {
	root := t.TempDir()
	alpha, beta := newEnv(t, root, "alpha"), newEnv(t, root, "beta")
	for _, c := range []struct {
		machines []smolvm.Machine
		state    smolvm.State
	}{
		{nil, ""},
		{[]smolvm.Machine{{Name: beta.VM(), State: smolvm.Running}}, ""},
		{[]smolvm.Machine{{Name: alpha.VM(), State: smolvm.Stopped}}, smolvm.Stopped},
		{[]smolvm.Machine{{Name: alpha.VM(), State: smolvm.Created}}, smolvm.Created},
		{[]smolvm.Machine{{Name: beta.VM(), State: smolvm.Stopped}, {Name: alpha.VM(), State: smolvm.Running}}, smolvm.Running},
	} {
		if got := alpha.VMState(c.machines); got != c.state {
			t.Errorf("VMState(%+v) = %q, want %q", c.machines, got, c.state)
		}
		if got := alpha.Running(c.machines); got != (c.state == smolvm.Running) {
			t.Errorf("Running(%+v) = %v", c.machines, got)
		}
	}
}

func TestUnclaimedFindsDevenvVMsOfNoEnvironment(t *testing.T) {
	root := t.TempDir()
	alpha := newEnv(t, root, "alpha")
	other := newEnv(t, t.TempDir(), "alpha")
	machines := []smolvm.Machine{{Name: alpha.VM()}, {Name: other.VM()}, {Name: "unrelated"}}

	got := state.Unclaimed([]state.Env{alpha}, machines)

	if len(got) != 1 || got[0].Name != other.VM() {
		t.Errorf("Unclaimed() = %+v", got)
	}
}

func TestPortsAreRecordedInTheEnvDir(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	if _, err := env.ReadPorts(); err == nil || !strings.Contains(err.Error(), "no ports") {
		t.Errorf("before WritePorts: err = %v", err)
	}
	want := state.Ports{MgmtAPI: 1, WorkloadAPI: 2, Registry: 3}

	if err := env.WritePorts(want); err != nil {
		t.Fatal(err)
	}

	if got, err := newEnv(t, filepath.Dir(env.Dir), "alpha").ReadPorts(); err != nil || got != want {
		t.Errorf("ReadPorts() = %+v, %v; want %+v", got, err, want)
	}
}

func TestStatusSaysWhetherARunningEnvironmentIsReady(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	if err := os.MkdirAll(env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	running := []smolvm.Machine{{Name: env.VM(), State: smolvm.Running}}
	stopped := []smolvm.Machine{{Name: env.VM(), State: smolvm.Stopped}}
	if got := env.Status(nil); got != "none" {
		t.Errorf("without a VM: %q", got)
	}
	if got := env.Status(running); got != "running (not ready)" {
		t.Errorf("before MarkReady: %q", got)
	}

	if err := env.MarkReady(); err != nil {
		t.Fatal(err)
	}

	if !env.Ready() {
		t.Error("not Ready after MarkReady")
	}
	if got := env.Status(running); got != "running" {
		t.Errorf("after MarkReady: %q", got)
	}
	if got := env.Status(stopped); got != "stopped" {
		t.Errorf("stopped: %q", got)
	}
}

func TestFreePortsAreDistinctAndFree(t *testing.T) {
	p, err := state.FreePorts()
	if err != nil {
		t.Fatal(err)
	}
	ports := []int{p.MgmtAPI, p.WorkloadAPI, p.Registry}
	for i, port := range ports {
		for _, other := range ports[:i] {
			if port == other {
				t.Errorf("FreePorts() = %+v repeats %d", p, port)
			}
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Errorf("port %d: %v", port, err)
			continue
		}
		l.Close()
	}
}

func TestListFindsEnvironmentDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"beta", "alpha"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "Not_A_Name"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	envs, err := state.List(root)

	if err != nil || len(envs) != 2 || envs[0].Name != "alpha" || envs[1].Name != "beta" {
		t.Errorf("List() = %+v, %v", envs, err)
	}
}

func TestListOfMissingRootIsEmpty(t *testing.T) {
	if envs, err := state.List(filepath.Join(t.TempDir(), "missing")); err != nil || len(envs) != 0 {
		t.Errorf("List() = %+v, %v", envs, err)
	}
}

func TestExistingPicksTheOnlyEnvironmentWhenNameIsEmpty(t *testing.T) {
	root := t.TempDir()
	if _, err := state.Existing(root, "", nil); err == nil || !strings.Contains(err.Error(), "no environments in") {
		t.Errorf("no environments: err = %v", err)
	}
	_ = os.MkdirAll(filepath.Join(root, "alpha"), 0o700)
	if env, err := state.Existing(root, "", nil); err != nil || env.Name != "alpha" {
		t.Errorf("Existing() = %+v, %v", env, err)
	}
}

func TestExistingPicksTheOnlyRunningEnvironmentWhenNameIsEmpty(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "alpha"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "beta"), 0o700)
	alpha, beta := newEnv(t, root, "alpha"), newEnv(t, root, "beta")
	if _, err := state.Existing(root, "", nil); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("none running: err = %v, want a hint to pass --name", err)
	}

	machines := []smolvm.Machine{{Name: alpha.VM(), State: smolvm.Stopped}, {Name: beta.VM(), State: smolvm.Running}}
	if env, err := state.Existing(root, "", machines); err != nil || env.Name != "beta" {
		t.Errorf("beta running: Existing() = %+v, %v", env, err)
	}

	machines[0].State = smolvm.Running
	if _, err := state.Existing(root, "", machines); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("both running: err = %v, want a hint to pass --name", err)
	}
}

func TestExistingFindsANamedEnvironment(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "beta"), 0o700)
	if env, err := state.Existing(root, "beta", nil); err != nil || env != newEnv(t, root, "beta") {
		t.Errorf("Existing(beta) = %+v, %v", env, err)
	}
	if _, err := state.Existing(root, "gamma", nil); err == nil || !strings.Contains(err.Error(), "no environment gamma") {
		t.Errorf("Existing(gamma): err = %v", err)
	}
}

func TestKubeconfigOfARunningEnvironment(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	if err := os.MkdirAll(env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "workload.kubeconfig"), []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopped := []smolvm.Machine{{Name: env.VM(), State: smolvm.Stopped}}
	if _, err := env.Kubeconfig("workload", stopped); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("stopped: err = %v", err)
	}
	running := []smolvm.Machine{{Name: env.VM(), State: smolvm.Running}}
	if _, err := env.Kubeconfig("mgmt", running); err == nil || !strings.Contains(err.Error(), "no mgmt kubeconfig") {
		t.Errorf("not written: err = %v", err)
	}
	if got, err := env.Kubeconfig("workload", running); err != nil || string(got) != "config" {
		t.Errorf("Kubeconfig(workload) = %q, %v", got, err)
	}
	if _, err := env.Kubeconfig("../lock", running); err == nil || !strings.Contains(err.Error(), "mgmt or workload") {
		t.Errorf("Kubeconfig(../lock): err = %v", err)
	}
}
