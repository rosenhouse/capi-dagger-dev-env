// Package build compiles a consumer's commands into images for the engine's platform.
package build

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"dagger.io/dagger"
)

const (
	golangImage = "golang:1.26.1@sha256:cd78d88e00afadbedd272f977d375a6247455f3a4b1178f8ae8bbcb201743a8a"
	baseImage   = "gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3"
)

// Spec says what to build from a consumer's module.
type Spec struct {
	// Root is the module's directory.
	Root string
	// Commands are main packages, such as "./cmd/hello".
	Commands []string
	// ConfigDirs are the package config directories under Root.
	ConfigDirs []string
}

// ImageName names a command's image after its directory.
func ImageName(command string) string { return path.Base(command) }

// Source selects the module's Go source under root, without tests.
// Each evaluation rereads the host, so a long-lived session sees edits.
func Source(c *dagger.Client, root string) *dagger.Directory {
	return c.Host().Directory(root, dagger.HostDirectoryOpts{
		Include: []string{"go.mod", "go.sum", "**/*.go"},
		Exclude: []string{"**/*_test.go", ".devenv/"},
		NoCache: true,
	})
}

// Config selects the config directories dirs under root. Each evaluation rereads the host.
func Config(c *dagger.Client, root string, dirs []string) *dagger.Directory {
	include := make([]string, len(dirs))
	for i, dir := range dirs {
		include[i] = path.Clean(dir) + "/"
	}
	return c.Host().Directory(root, dagger.HostDirectoryOpts{Include: include, NoCache: true})
}

// Build is the commands built from one snapshot of the host, and that snapshot's package config.
type Build struct {
	Source   *dagger.Directory
	Binaries *dagger.Directory
	Config   *dagger.Directory
	Version  string
}

// FromHost builds spec from a snapshot of the host. An empty version names the build by the source's content.
func FromHost(ctx context.Context, c *dagger.Client, spec Spec, version string) (Build, error) {
	src, config, err := Snapshot(ctx, c, spec)
	if err != nil {
		return Build{}, err
	}
	if version == "" {
		if version, err = Version(ctx, src); err != nil {
			return Build{}, err
		}
	}
	return Build{Source: src, Binaries: Binaries(c, src, version, spec.Commands), Config: config, Version: version}, nil
}

// Snapshot pins the current content of Source and Config, so a save during a build does not change them.
func Snapshot(ctx context.Context, c *dagger.Client, spec Spec) (src, config *dagger.Directory, err error) {
	if src, err = Source(c, spec.Root).Sync(ctx); err != nil {
		return nil, nil, err
	}
	config, err = Config(c, spec.Root, spec.ConfigDirs).Sync(ctx)
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

// Binaries builds commands, with version in their main.version. Its "built-at" file holds the
// time of the build, which a cached build keeps.
func Binaries(c *dagger.Client, src *dagger.Directory, version string, commands []string) *dagger.Directory {
	args := []string{"sh", "-c", `go build "$@" && date +%s%N > /out/built-at`, "go-build",
		"-trimpath", "-ldflags", "-X main.version=" + version, "-o", "/out/"}
	args = append(args, commands...)
	return c.Container().From(golangImage).
		WithMountedCache("/go/pkg/mod", c.CacheVolume("devenv-go-mod")).
		WithMountedCache("/root/.cache/go-build", c.CacheVolume("devenv-go-build")).
		WithEnvVariable("CGO_ENABLED", "0").
		WithDirectory("/src", src).
		WithWorkdir("/src").
		WithExec(args).
		Directory("/out")
}

// Images packages each of commands, built into bin, as an image named by ImageName.
func Images(c *dagger.Client, bin *dagger.Directory, commands []string) map[string]*dagger.Container {
	images := map[string]*dagger.Container{}
	for _, command := range commands {
		name := ImageName(command)
		images[name] = c.Container().From(baseImage).
			WithFile("/"+name, bin.File(name)).
			WithEntrypoint([]string{"/" + name})
	}
	return images
}
