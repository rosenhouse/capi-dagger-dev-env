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
	"time"

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
// It retries briefly, because Running probes the lock.
func (e Env) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(e.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for range 5 {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("environment %s is already running", e.Name)
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// Running reports whether a process holds the environment's lock.
func (e Env) Running() (bool, error) {
	f, err := os.Open(filepath.Join(e.Dir, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

// SocketPath is where the environment's up process listens for requests.
// It lives in the temporary directory, under a hash of Dir, because Unix socket paths are short on macOS.
func (e Env) SocketPath() string {
	dirHash := sha256.Sum256([]byte(e.Dir))
	return filepath.Join(os.TempDir(), "devenv-"+hex.EncodeToString(dirHash[:8])+".sock")
}

// Kubeconfig returns the kubeconfig of the running environment's mgmt or workload cluster.
func (e Env) Kubeconfig(cluster string) ([]byte, error) {
	if cluster != "mgmt" && cluster != "workload" {
		return nil, fmt.Errorf("cluster %q is not mgmt or workload", cluster)
	}
	running, err := e.Running()
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, fmt.Errorf("environment %s is not running", e.Name)
	}
	kubeconfig, err := os.ReadFile(filepath.Join(e.Dir, cluster+".kubeconfig"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("environment %s has no %s kubeconfig yet", e.Name, cluster)
	}
	return kubeconfig, err
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

// Existing returns the environment called name under root. If name is empty,
// it returns the only environment there, or else the only running one.
func Existing(root, name string) (Env, error) {
	if name != "" {
		return New(root, name)
	}
	envs, err := List(root)
	if err != nil {
		return Env{}, err
	}
	if len(envs) == 1 {
		return envs[0], nil
	}
	var running []Env
	for _, env := range envs {
		if ok, err := env.Running(); err != nil {
			return Env{}, err
		} else if ok {
			running = append(running, env)
		}
	}
	if len(running) == 1 {
		return running[0], nil
	}
	return Env{}, fmt.Errorf("found %d environments in %s, %d of them running; pass --name", len(envs), root, len(running))
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
