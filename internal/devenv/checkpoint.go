package devenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// platformInputs are what shapes the platform in a VM. The platform checkpoint's key hashes them.
type platformInputs struct {
	SmolVM    string               `json:"smolvm"`
	Contract  string               `json:"contract"`
	Machine   smolvm.MachineConfig `json:"machine"`
	Downloads []infra.Download     `json:"downloads"`
	Images    []string             `json:"images"`
	Scripts   []string             `json:"scripts"`
}

// describePlatform returns the platform's inputs on a host with the given CPU contract.
func describePlatform(contract string) (platformInputs, error) {
	d, err := downloads()
	if err != nil {
		return platformInputs{}, err
	}
	scripts, err := platform.Scripts(WorkloadCluster, WorkloadNamespace)
	if err != nil {
		return platformInputs{}, err
	}
	return platformInputs{
		SmolVM:    smolvm.Version,
		Contract:  contract,
		Machine:   infra.MachineConfig(state.Ports{}),
		Downloads: d,
		Images:    infra.Images(),
		Scripts:   append(infra.Scripts(WorkloadCluster, WorkloadNamespace), scripts...),
	}, nil
}

func (in platformInputs) key() string {
	data, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// PlatformInputs returns the inputs of this host's platform checkpoint, and their key.
func PlatformInputs() (key string, inputs []byte, err error) {
	in, err := hostPlatform()
	if err != nil {
		return "", nil, err
	}
	inputs, err = json.MarshalIndent(in, "", "  ")
	return in.key(), inputs, err
}

func hostPlatform() (platformInputs, error) {
	contract, err := smolvm.HostContract()
	if err != nil {
		return platformInputs{}, err
	}
	return describePlatform(contract)
}

// platformCache holds a platform checkpoint per key, beside the inputs that the key hashes.
type platformCache struct{ dir string }

func (o Options) platformCache() platformCache {
	return platformCache{dir: filepath.Join(o.CacheDir, "platform")}
}

func (c platformCache) checkpoint(key string) string {
	return filepath.Join(c.dir, key+".checkpoint")
}

// lookup returns the checkpoint for key, or an error that says why there is none.
func (c platformCache) lookup(key string) (string, error) {
	path := c.checkpoint(key)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no platform checkpoint %s", path)
	} else if err != nil {
		return "", err
	}
	return path, nil
}

// save has capture write a checkpoint to a temporary file, then makes it in.key()'s entry.
func (c platformCache) save(in platformInputs, capture func(file string) error) (string, error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", err
	}
	key := in.key()
	tmp := filepath.Join(c.dir, "."+key+".checkpoint")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := capture(tmp); err != nil {
		return "", errors.Join(err, removeIfExists(tmp))
	}
	inputs, err := json.MarshalIndent(in, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(c.dir, key+".json"), inputs, 0o644)
	}
	path := c.checkpoint(key)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return "", errors.Join(err, removeIfExists(tmp))
	}
	return path, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SavePlatform brings up the platform in a new VM, captures it as this host's platform checkpoint, and deletes the VM.
// It refuses to replace a checkpoint unless replace is set.
func SavePlatform(ctx context.Context, o Options, replace bool) (string, error) {
	if err := checkVM(ctx, o); err != nil {
		return "", err
	}
	in, err := hostPlatform()
	if err != nil {
		return "", err
	}
	cache := o.platformCache()
	if path, err := cache.lookup(in.key()); err == nil && !replace {
		return "", fmt.Errorf("%s exists; replace it with: %s", path, o.hint("platform save --force"))
	}
	env, err := state.New(o.CacheDir, "platform-save")
	if err != nil {
		return "", err
	}
	e, err := open(env, o)
	if err != nil {
		return "", err
	}
	e.branchable = true
	machines, err := o.SmolVM.List(ctx)
	if err == nil {
		err = e.forget()
	}
	if err == nil {
		err = e.coldPlatform(ctx, e.VMState(machines), func() {})
	}
	var path string
	if err == nil {
		path, err = cache.save(in, func(file string) error { return e.capture(ctx, file) })
	}
	if err != nil {
		return "", errors.Join(e.fail(ctx, err), e.Close())
	}
	_, err = e.Delete(ctx, true)
	return path, errors.Join(err, e.Close())
}

// capture checkpoints the VM to file, and deletes the VM, which runs slowly after a capture.
func (e *Environment) capture(ctx context.Context, file string) error {
	if err := e.stage("prepare capture", func() error {
		filled, err := e.vm.PrepareCapture(ctx, hostAvailableMiB())
		e.progress(fmt.Sprintf("zero-filled %d MiB of guest memory", filled))
		return err
	}); err != nil {
		return err
	}
	if err := e.stage("capture", func() error { return e.opts.SmolVM.Checkpoint(ctx, e.vm.Name, file) }); err != nil {
		return err
	}
	if info, err := os.Stat(file); err == nil {
		e.progress(fmt.Sprintf("checkpoint: %d MiB", info.Size()>>20))
	}
	return e.vm.Delete(ctx)
}

// hostAvailableMiB returns the host's available memory, or 0 where it cannot tell.
func hostAvailableMiB() int {
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return memAvailableMiB(string(meminfo))
}

func memAvailableMiB(meminfo string) int {
	for _, line := range strings.Split(meminfo, "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kib, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			if err != nil {
				return 0
			}
			return kib >> 10
		}
	}
	return 0
}
