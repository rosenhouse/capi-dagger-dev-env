package state_test

import (
	"os"
	"path/filepath"
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
	a, err := state.New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := state.New(t.TempDir(), "")
	if errs := validation.IsDNS1123Label(a.Name); len(errs) > 0 {
		t.Errorf("generated name %q: %v", a.Name, errs)
	}
	if a.Name == b.Name {
		t.Errorf("generated the same name twice: %q", a.Name)
	}
}

func TestEnvDirIsKeyedByName(t *testing.T) {
	root := t.TempDir()
	env, _ := state.New(root, "alpha")
	if env.Dir != filepath.Join(root, "alpha") {
		t.Errorf("dir = %q", env.Dir)
	}
}

func TestWriteKubeconfigTargetsLocalhostPort(t *testing.T) {
	env, _ := state.New(t.TempDir(), "alpha")
	in := clientcmdapi.NewConfig()
	in.Clusters["kind-mgmt"] = &clientcmdapi.Cluster{Server: "https://0.0.0.0:6443", TLSServerName: "docker", CertificateAuthorityData: []byte("ca")}
	in.AuthInfos["kind-mgmt"] = &clientcmdapi.AuthInfo{ClientCertificateData: []byte("cert")}
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
	cluster := out.Clusters["kind-mgmt"]
	if cluster.Server != "https://localhost:41234" || cluster.TLSServerName != "" || string(cluster.CertificateAuthorityData) != "ca" {
		t.Errorf("cluster = %+v", cluster)
	}
	if string(out.AuthInfos["kind-mgmt"].ClientCertificateData) != "cert" {
		t.Error("lost client certificate")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}
