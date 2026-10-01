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
	// Config holds the files under config/<package>/, by package and then by slash-separated path.
	Config map[string]map[string][]byte
}

// Build compiles Commands under root for linux/goarch into outDir, with main.version set to version,
// or to the source's Version if version is empty. It ignores the host's build flags. It fails if the
// commands import a package that Version leaves out, or if the source changes during the build.
func Build(ctx context.Context, root, goarch, version, outDir string) (Snapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Snapshot{}, err
	}
	if outDir, err = filepath.Abs(outDir); err != nil {
		return Snapshot{}, err
	}
	snap, err := read(root)
	if err != nil {
		return Snapshot{}, err
	}
	env := append(os.Environ(), "PWD="+root, "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off",
		"GOFLAGS=", "GOEXPERIMENT=", "GOAMD64=", "GOARM64=")
	var pkgs []string
	for _, name := range Commands {
		pkgs = append(pkgs, "./cmd/"+name)
	}
	if err := checkImports(ctx, root, env, pkgs); err != nil {
		return Snapshot{}, err
	}
	args := append([]string{"build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-X main.version=" + cmp.Or(version, snap.Version), "-o", outDir + "/"}, pkgs...)
	if _, err := goCmd(ctx, root, env, args...); err != nil {
		return Snapshot{}, err
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

// checkImports fails if pkgs import a package from neither the module cache nor Version's files.
func checkImports(ctx context.Context, root string, env, pkgs []string) error {
	modCache, err := goCmd(ctx, root, env, "env", "GOMODCACHE")
	if err != nil {
		return err
	}
	dirs, err := goCmd(ctx, root, env, append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}"}, pkgs...)...)
	if err != nil {
		return err
	}
	for _, dir := range strings.Split(strings.TrimSpace(dirs), "\n") {
		if within(strings.TrimSpace(modCache), dir) {
			continue
		}
		if rel, err := filepath.Rel(root, dir); err != nil || !versioned(filepath.ToSlash(rel)) {
			return fmt.Errorf("the commands import %s, which Version leaves out", dir)
		}
	}
	return nil
}

func goCmd(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir, cmd.Env = dir, env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %w\n%s", args[0], err, stderr.String())
	}
	return string(out), nil
}

func read(root string) (Snapshot, error) {
	version, err := Version(root)
	if err != nil {
		return Snapshot{}, err
	}
	config := map[string]map[string][]byte{}
	fsys := os.DirFS(filepath.Join(root, "config"))
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		pkg, rest, ok := strings.Cut(path, "/")
		if !ok {
			return nil
		}
		if config[pkg] == nil {
			config[pkg] = map[string][]byte{}
		}
		config[pkg][rest], err = fs.ReadFile(fsys, path)
		return err
	})
	return Snapshot{Version: version, Config: config}, err
}

// Version names the first-party source under root by its content: go.mod, go.sum, and api/, cmd/ and
// internal/ without the devenv orchestrator and tests, so editing those keeps the version.
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
			case d.IsDir() && !versioned(path):
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

// versioned reports whether Version reads the directory dir, a slash-separated path below the root.
func versioned(dir string) bool {
	top, _, _ := strings.Cut(dir, "/")
	return (top == "api" || top == "cmd" || top == "internal") &&
		!within("cmd/devenv", dir) && !within("internal/devenv", dir)
}

// within reports whether path is parent or lies below it.
func within(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && filepath.IsLocal(rel)
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
