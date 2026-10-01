// Package hostbuild cross-compiles the first-party commands for linux with the host's Go toolchain.
package hostbuild

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Commands are the first-party binaries. Each ships as an image of the same name.
var Commands = []string{"addon-manager", "greeting-syncer", "greeting-controller", "hello"}

// Snapshot is the source's Version and the package manifests, read together.
type Snapshot struct {
	Version string
	// Config holds each file under config/ by its slash-separated path below config/.
	Config map[string][]byte
}

// Build compiles Commands under root for linux/goarch into outDir, with main.version set to version,
// or to the source's Version if version is empty. It fails if the source changes during the build.
func Build(ctx context.Context, root, goarch, version, outDir string) (Snapshot, error) {
	snap, err := read(root)
	if err != nil {
		return Snapshot{}, err
	}
	args := []string{"build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-X main.version=" + cmp.Or(version, snap.Version), "-o", outDir + "/"}
	for _, name := range Commands {
		args = append(args, "./cmd/"+name)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return Snapshot{}, fmt.Errorf("go build: %w\n%s", err, out)
	}
	after, err := Version(root)
	if err != nil {
		return Snapshot{}, err
	}
	if after != snap.Version {
		return Snapshot{}, errors.New("source changed during the build")
	}
	return snap, nil
}

func read(root string) (Snapshot, error) {
	version, err := Version(root)
	if err != nil {
		return Snapshot{}, err
	}
	config := map[string][]byte{}
	fsys := os.DirFS(filepath.Join(root, "config"))
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		config[path], err = fs.ReadFile(fsys, path)
		return err
	})
	return Snapshot{Version: version, Config: config}, err
}

// Version names the first-party source under root by its content: go.mod, go.sum, api/, cmd/ and
// internal/, without the devenv orchestrator and tests, so editing those keeps the version.
func Version(root string) (string, error) {
	fsys := os.DirFS(root)
	h := sha256.New()
	for _, top := range []string{"go.mod", "go.sum", "api", "cmd", "internal"} {
		err := fs.WalkDir(fsys, top, func(path string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case err != nil:
				return err
			case d.IsDir() && (path == "cmd/devenv" || path == "internal/devenv"):
				return fs.SkipDir
			case d.IsDir() || strings.HasSuffix(path, "_test.go"):
				return nil
			}
			b, err := fs.ReadFile(fsys, path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
			h.Write(b)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// ModuleRoot returns the nearest directory at or above dir that holds a go.mod.
func ModuleRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if d == filepath.Dir(d) {
			return "", fmt.Errorf("no go.mod at or above %s", dir)
		}
	}
}
