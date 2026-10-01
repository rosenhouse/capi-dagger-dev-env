// Package state holds the per-environment state that lives on the host.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// Holder returns the ID of the process that holds the environment's lock, or 0 if none does.
func (e Env) Holder() (int, error) {
	f, err := os.Open(filepath.Join(e.Dir, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return 0, err
	}
	pid, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(pid))
}

// List returns the environments under root, sorted by name.
func List(root string) ([]Env, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envs []Env
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		env, err := New(root, entry.Name())
		if err != nil {
			continue
		}
		envs = append(envs, env)
	}
	return envs, nil
}

// Existing returns the environment called name under root, or the only one there if name is empty.
func Existing(root, name string) (Env, error) {
	envs, err := List(root)
	if err != nil {
		return Env{}, err
	}
	if name == "" {
		if len(envs) != 1 {
			return Env{}, fmt.Errorf("found %d environments in %s; pass --name", len(envs), root)
		}
		return envs[0], nil
	}
	for _, env := range envs {
		if env.Name == name {
			return env, nil
		}
	}
	return Env{}, fmt.Errorf("no environment %s in %s", name, root)
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
