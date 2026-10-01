// Package state holds the per-environment state that lives on the host.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
)

// Env is one development environment.
type Env struct {
	Name string
	Dir  string
	// ID names host-wide resources such as the VM. It differs between state dirs that reuse a Name.
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
	if strings.Contains(name, "--") {
		return Env{}, fmt.Errorf("invalid environment name %q: smolvm rejects consecutive hyphens", name)
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
			return nil, fmt.Errorf("another devenv command is using environment %s", e.Name)
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// VM names the environment's smolvm machine.
func (e Env) VM() string { return "devenv-" + e.ID }

// VMState returns the state of the environment's VM among machines, or "" if there is none.
func (e Env) VMState(machines []smolvm.Machine) smolvm.State {
	for _, m := range machines {
		if m.Name == e.VM() {
			return m.State
		}
	}
	return ""
}

// Running reports whether the environment's VM is running among machines.
func (e Env) Running(machines []smolvm.Machine) bool {
	return e.VMState(machines) == smolvm.Running
}

// Ports are the host ports that publish the guest's API servers and registry.
type Ports struct {
	MgmtAPI     int `json:"mgmtAPI"`
	WorkloadAPI int `json:"workloadAPI"`
	Registry    int `json:"registry"`
}

// FreePorts returns ports that are free on the host's loopback address now.
func FreePorts() (Ports, error) {
	var ports [3]int
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return Ports{}, err
		}
		defer l.Close()
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	return Ports{MgmtAPI: ports[0], WorkloadAPI: ports[1], Registry: ports[2]}, nil
}

func (e Env) WritePorts(p Ports) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.Dir, "ports.json"), data, 0o600)
}

func (e Env) Ports() (Ports, error) {
	data, err := os.ReadFile(filepath.Join(e.Dir, "ports.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Ports{}, fmt.Errorf("environment %s has no ports recorded", e.Name)
	}
	if err != nil {
		return Ports{}, err
	}
	var p Ports
	return p, json.Unmarshal(data, &p)
}

// Kubeconfig returns the kubeconfig of the environment's mgmt or workload cluster, if its VM is running among machines.
func (e Env) Kubeconfig(cluster string, machines []smolvm.Machine) ([]byte, error) {
	if cluster != "mgmt" && cluster != "workload" {
		return nil, fmt.Errorf("cluster %q is not mgmt or workload", cluster)
	}
	if !e.Running(machines) {
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

// Existing returns the environment called name under root. If name is empty, it returns
// the only environment there, or else the only one whose VM is running among machines.
func Existing(root, name string, machines []smolvm.Machine) (Env, error) {
	envs, err := List(root)
	if err != nil {
		return Env{}, err
	}
	if name != "" {
		for _, env := range envs {
			if env.Name == name {
				return env, nil
			}
		}
		return Env{}, fmt.Errorf("no environment %s in %s", name, root)
	}
	if len(envs) == 0 {
		return Env{}, fmt.Errorf("no environments in %s", root)
	}
	if len(envs) == 1 {
		return envs[0], nil
	}
	var running []Env
	for _, env := range envs {
		if env.Running(machines) {
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
