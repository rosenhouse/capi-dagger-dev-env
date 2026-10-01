// Package state holds the per-environment state that lives on the host.
package state

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
)

// Env is one development environment. Its Name keys everything mutable.
type Env struct {
	Name string
	Dir  string
}

// New returns the environment called name under root, or a randomly named one if name is empty.
func New(root, name string) (Env, error) {
	if name == "" {
		name = "env-" + strings.ToLower(rand.Text()[:6])
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return Env{}, fmt.Errorf("invalid environment name %q: %s", name, strings.Join(errs, "; "))
	}
	return Env{Name: name, Dir: filepath.Join(root, name)}, nil
}

// WriteKubeconfig rewrites every server in kubeconfig to a localhost port and writes it to the env dir.
func (e Env) WriteKubeconfig(cluster string, kubeconfig []byte, port int) (string, error) {
	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return "", err
	}
	for _, c := range cfg.Clusters {
		c.Server = fmt.Sprintf("https://localhost:%d", port)
		c.TLSServerName = ""
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(e.Dir, cluster+".kubeconfig")
	return path, clientcmd.WriteToFile(*cfg, path)
}
