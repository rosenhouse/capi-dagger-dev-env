// Package state holds the per-environment state that lives on the host.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Env is one development environment.
type Env struct {
	Name string
	Dir  string
	// ID keys engine-wide resources such as cache volumes. It differs between state dirs that reuse a Name.
	ID string
}

// New returns the environment called name under root, or a randomly named one if name is empty.
func New(root, name string) (Env, error) {
	if name == "" {
		name = "env-" + strings.ToLower(rand.Text()[:6])
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return Env{}, fmt.Errorf("invalid environment name %q: %s", name, strings.Join(errs, "; "))
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Env{}, err
	}
	rootHash := sha256.Sum256([]byte(root))
	return Env{Name: name, Dir: filepath.Join(root, name), ID: name + "-" + hex.EncodeToString(rootHash[:4])}, nil
}

// Lock fails if another process holds the environment. The lock lasts until unlock or process exit.
func (e Env) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(e.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("environment %s is already running", e.Name)
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// WriteKubeconfig points kubeconfig at a localhost port, names its entries <env>-<cluster>, and writes it to the env dir.
func (e Env) WriteKubeconfig(cluster string, kubeconfig []byte, port int) (string, error) {
	in, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return "", err
	}
	current := in.Contexts[in.CurrentContext]
	if current == nil {
		return "", errors.New("kubeconfig has no current context")
	}
	name := e.Name + "-" + cluster
	server := *in.Clusters[current.Cluster]
	server.Server = fmt.Sprintf("https://localhost:%d", port)
	server.TLSServerName = ""
	out := clientcmdapi.NewConfig()
	out.Clusters[name] = &server
	out.AuthInfos[name] = in.AuthInfos[current.AuthInfo]
	out.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	out.CurrentContext = name

	path := filepath.Join(e.Dir, cluster+".kubeconfig")
	return path, clientcmd.WriteToFile(*out, path)
}
