package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
