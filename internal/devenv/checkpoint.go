package devenv

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// ownPlatformSource is devenv's code that brings up the platform cold and captures it.
//
//go:embed platform.go capture.go
var ownPlatformSource embed.FS

// platformSources are the code that shapes the platform in a VM, by directory.
var platformSources = map[string]fs.FS{".": ownPlatformSource, "infra": infra.Source, "platform": platform.Source, "smolvm": smolvm.Source}

// platformInputs are what shapes the platform in a VM. The platform checkpoint's key hashes them.
type platformInputs struct {
	Contract string `json:"contract"`
	Platform string `json:"platform"`
	// Sources holds the sha256 of each file of platformSources but their tests.
	Sources map[string]string `json:"sources"`
}

// describePlatform returns the platform's inputs on a host with the given CPU contract.
func describePlatform(contract string) (platformInputs, error) {
	in := platformInputs{Contract: contract, Platform: runtime.GOOS + "/" + runtime.GOARCH, Sources: map[string]string{}}
	for dir, fsys := range platformSources {
		err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(name, "_test.go") {
				return err
			}
			data, err := fs.ReadFile(fsys, name)
			sum := sha256.Sum256(data)
			in.Sources[path.Join(dir, name)] = hex.EncodeToString(sum[:])
			return err
		})
		if err != nil {
			return platformInputs{}, err
		}
	}
	return in, nil
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

// platformCache holds a platform checkpoint per key, beside an entry that says when it was captured from what.
type platformCache struct{ dir string }

type platformEntry struct {
	CapturedAt time.Time      `json:"captured_at"`
	Inputs     platformInputs `json:"inputs"`
}

func (o Options) platformCache() platformCache {
	return platformCache{dir: filepath.Join(o.CacheDir, "platform")}
}

func (c platformCache) checkpoint(key string) string {
	return filepath.Join(c.dir, key+".checkpoint")
}

func (c platformCache) entry(key string) string {
	return filepath.Join(c.dir, key+".json")
}

// lookup returns the checkpoint for key and when it was captured, or an error that says why there is none.
func (c platformCache) lookup(key string) (string, time.Time, error) {
	path := c.checkpoint(key)
	if _, err := os.Stat(path); err != nil {
		return path, time.Time{}, err
	}
	var entry platformEntry
	data, err := os.ReadFile(c.entry(key))
	if err == nil {
		err = json.Unmarshal(data, &entry)
	}
	if err != nil {
		return path, time.Time{}, fmt.Errorf("%s has no valid entry: %v", path, err)
	}
	return path, entry.CapturedAt, nil
}

// save has capture write a checkpoint to a temporary file, then makes that file the entry for in's key.
// It removes the other keys' entries, which a save on the same host replaces, and what failed saves left.
func (c platformCache) save(in platformInputs, capture func(file string) error) (string, error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", err
	}
	key := in.key()
	tmp := filepath.Join(c.dir, "."+key+".checkpoint")
	if err := removeIfExists(tmp); err != nil {
		return "", err
	}
	if err := capture(tmp); err != nil {
		return "", errors.Join(err, removeIfExists(tmp))
	}
	entry, err := json.MarshalIndent(platformEntry{CapturedAt: time.Now(), Inputs: in}, "", "  ")
	if err == nil {
		err = os.WriteFile(c.entry(key), entry, 0o644)
	}
	path := c.checkpoint(key)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return "", errors.Join(err, removeIfExists(tmp))
	}
	return path, c.removeAllBut(key)
}

func (c platformCache) removeAllBut(key string) error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && e.Name() != key+".checkpoint" && e.Name() != key+".json" {
			if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// pin hard-links checkpoint at a path of the VM's own.
func (c platformCache) pin(checkpoint, vm string) (string, error) {
	pinned := filepath.Join(c.dir, "restoring", vm+".checkpoint")
	if err := os.MkdirAll(filepath.Dir(pinned), 0o755); err != nil {
		return "", err
	}
	if err := removeIfExists(pinned); err != nil {
		return "", err
	}
	return pinned, os.Link(checkpoint, pinned)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// platformSaveEnv is the environment that SavePlatform captures.
func (o Options) platformSaveEnv() (state.Env, error) { return state.New(o.CacheDir, "platform-save") }

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
	if path, _, err := cache.lookup(in.key()); err == nil && !replace {
		return "", fmt.Errorf("%s exists; replace it with: %s", path, o.hint("platform save --force"))
	}
	env, err := o.platformSaveEnv()
	if err != nil {
		return "", err
	}
	e, err := open(env, o)
	if err != nil {
		return "", err
	}
	machines, err := o.SmolVM.List(ctx)
	if err == nil {
		err = e.forget()
	}
	if err == nil {
		err = e.coldPlatform(ctx, e.VMState(machines), func() {}, true)
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
