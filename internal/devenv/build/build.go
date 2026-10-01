// Package build compiles first-party commands into images for the engine's platform.
package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dagger.io/dagger"
)

const (
	golangImage = "golang:1.26.1@sha256:cd78d88e00afadbedd272f977d375a6247455f3a4b1178f8ae8bbcb201743a8a"
	baseImage   = "gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3"
)

// Commands are the first-party binaries. Each ships as an image of the same name.
var Commands = []string{"addon-manager", "greeting-syncer", "greeting-controller", "hello"}

// Source selects the Go source of the first-party commands under root.
// It leaves out the devenv orchestrator and tests, so editing them keeps the build cached.
// Each evaluation rereads the host, so a long-lived session sees edits.
func Source(c *dagger.Client, root string) *dagger.Directory {
	return c.Host().Directory(root, dagger.HostDirectoryOpts{
		Include: []string{"go.mod", "go.sum", "api/", "cmd/", "internal/"},
		Exclude: []string{"cmd/devenv/", "internal/devenv/", "**/*_test.go"},
		NoCache: true,
	})
}

// Config is the directory of package manifests under root. Each evaluation rereads the host.
func Config(c *dagger.Client, root string) *dagger.Directory {
	return c.Host().Directory(root+"/config", dagger.HostDirectoryOpts{NoCache: true})
}

// Snapshot pins the current content of Source and Config, so a save during a build does not change them.
func Snapshot(ctx context.Context, c *dagger.Client, root string) (src, config *dagger.Directory, err error) {
	if src, err = Source(c, root).Sync(ctx); err != nil {
		return nil, nil, err
	}
	config, err = Config(c, root).Sync(ctx)
	return src, config, err
}

// Version names a build of src by its content.
func Version(ctx context.Context, src *dagger.Directory) (string, error) {
	digest, err := src.Digest(ctx)
	if err != nil {
		return "", err
	}
	_, hex, _ := strings.Cut(digest, ":")
	return hex[:12], nil
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

// Images builds every command, with version in its main.version, and returns its image by name.
func Images(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
	args := []string{"go", "build", "-trimpath", "-ldflags", "-X main.version=" + version, "-o", "/out/"}
	for _, name := range Commands {
		args = append(args, "./cmd/"+name)
	}
	bin := c.Container().From(golangImage).
		WithMountedCache("/go/pkg/mod", c.CacheVolume("devenv-go-mod")).
		WithMountedCache("/root/.cache/go-build", c.CacheVolume("devenv-go-build")).
		WithEnvVariable("CGO_ENABLED", "0").
		WithDirectory("/src", src).
		WithWorkdir("/src").
		WithExec(args).
		Directory("/out")

	images := map[string]*dagger.Container{}
	for _, name := range Commands {
		images[name] = c.Container().From(baseImage).
			WithFile("/"+name, bin.File(name)).
			WithEntrypoint([]string{"/" + name})
	}
	return images
}
