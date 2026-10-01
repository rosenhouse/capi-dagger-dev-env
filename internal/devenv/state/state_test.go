package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func TestNewRejectsNamesThatAreNotDNSLabels(t *testing.T) {
	if _, err := state.New(t.TempDir(), "Not_A_Label"); err == nil {
		t.Error("accepted invalid name")
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

func TestLockRejectsASecondHolderUntilReleased(t *testing.T) {
	root := t.TempDir()
	unlock, err := newEnv(t, root, "alpha").Lock()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := newEnv(t, root, "alpha").Lock(); err == nil || !strings.Contains(err.Error(), "alpha is already running") {
		t.Errorf("second Lock: err = %v", err)
	}

	unlock()
	if _, err := newEnv(t, root, "alpha").Lock(); err != nil {
		t.Errorf("Lock after unlock: %v", err)
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

func TestLockWaitsOutABriefProbe(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	probe, err := os.Open(filepath.Join(env.Dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(30*time.Millisecond, func() { probe.Close() })

	unlock, err = env.Lock()

	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestRunningWhileLocked(t *testing.T) {
	root := t.TempDir()
	env := newEnv(t, root, "alpha")
	if running, err := env.Running(); err != nil || running {
		t.Errorf("before Lock: Running() = %v, %v", running, err)
	}

	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if running, err := newEnv(t, root, "alpha").Running(); err != nil || !running {
		t.Errorf("while locked: Running() = %v, %v", running, err)
	}

	unlock()
	if running, err := env.Running(); err != nil || running {
		t.Errorf("after unlock: Running() = %v, %v", running, err)
	}
}

func TestSocketPathIsShortAndUnique(t *testing.T) {
	t.Setenv("HOME", "/Users/a-rather-long-user-name")
	t.Setenv("XDG_CACHE_HOME", "")
	a := newEnv(t, t.TempDir(), strings.Repeat("a", 63))
	b := newEnv(t, t.TempDir(), strings.Repeat("a", 63))
	if len(a.SocketPath()) > 100 {
		t.Errorf("SocketPath() = %s is longer than macOS allows", a.SocketPath())
	}
	if a.SocketPath() == b.SocketPath() {
		t.Error("environments in different state dirs share a socket")
	}
	if strings.HasPrefix(a.SocketPath(), os.TempDir()) {
		t.Errorf("SocketPath() = %s is in the temporary directory, which macOS cleans", a.SocketPath())
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
	if _, err := state.Existing(root, ""); err == nil || !strings.Contains(err.Error(), "no environments in") {
		t.Errorf("no environments: err = %v", err)
	}
	_ = os.MkdirAll(filepath.Join(root, "alpha"), 0o700)
	if env, err := state.Existing(root, ""); err != nil || env.Name != "alpha" {
		t.Errorf("Existing() = %+v, %v", env, err)
	}
}

func TestExistingPicksTheOnlyRunningEnvironmentWhenNameIsEmpty(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "alpha"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "beta"), 0o700)
	if _, err := state.Existing(root, ""); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("none running: err = %v, want a hint to pass --name", err)
	}

	unlock, err := newEnv(t, root, "beta").Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if env, err := state.Existing(root, ""); err != nil || env.Name != "beta" {
		t.Errorf("beta running: Existing() = %+v, %v", env, err)
	}

	unlockAlpha, err := newEnv(t, root, "alpha").Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockAlpha()
	if _, err := state.Existing(root, ""); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("both running: err = %v, want a hint to pass --name", err)
	}
}

func TestExistingFindsANamedEnvironment(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "beta"), 0o700)
	if env, err := state.Existing(root, "beta"); err != nil || env != newEnv(t, root, "beta") {
		t.Errorf("Existing(beta) = %+v, %v", env, err)
	}
	if _, err := state.Existing(root, "gamma"); err == nil || !strings.Contains(err.Error(), "no environment gamma") {
		t.Errorf("Existing(gamma): err = %v", err)
	}
}

func TestKubeconfigOfARunningEnvironment(t *testing.T) {
	env := newEnv(t, t.TempDir(), "alpha")
	if _, err := env.Kubeconfig("mgmt"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("stopped: err = %v", err)
	}
	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := env.Kubeconfig("workload"); err == nil || !strings.Contains(err.Error(), "no workload kubeconfig") {
		t.Errorf("not written: err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "workload.kubeconfig"), []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := env.Kubeconfig("workload"); err != nil || string(got) != "config" {
		t.Errorf("Kubeconfig(workload) = %q, %v", got, err)
	}
	if _, err := env.Kubeconfig("../lock"); err == nil || !strings.Contains(err.Error(), "mgmt or workload") {
		t.Errorf("Kubeconfig(../lock): err = %v", err)
	}
}
