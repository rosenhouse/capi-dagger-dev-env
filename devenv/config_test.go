package devenv

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dagger.io/dagger"
)

func TestConfigValidation(t *testing.T) {
	valid := Config{
		Commands: []string{"./cmd/hello"},
		Packages: []Package{{Name: "hello", RefName: "hello.example.com", Config: "config/hello", Images: []string{"hello"}}},
	}
	if err := valid.validate(); err != nil {
		t.Errorf("valid config: %v", err)
	}
	for _, tc := range []struct {
		want   string
		change func(*Config)
	}{
		{"no packages", func(c *Config) { c.Packages = nil }},
		{"a package has no name", func(c *Config) { c.Packages[0].Name = "" }},
		{`package hello has no RefName`, func(c *Config) { c.Packages[0].RefName = "" }},
		{`package hello has no Config`, func(c *Config) { c.Packages[0].Config = "" }},
		{`two packages are called hello`, func(c *Config) { c.Packages = append(c.Packages, c.Packages[0]) }},
		{`package hello has an unknown target 2`, func(c *Config) { c.Packages[0].On = 2 }},
		{"command cmd/hello does not start with ./", func(c *Config) { c.Commands = []string{"cmd/hello"} }},
		{"commands ./hello and ./cmd/hello both build image hello", func(c *Config) { c.Commands = []string{"./hello", "./cmd/hello"} }},
	} {
		c := valid
		c.Packages = append([]Package(nil), valid.Packages...)
		tc.change(&c)
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.want, err)
		}
	}
}

func TestConfigValidationReportsEveryProblemOfAPackage(t *testing.T) {
	c := Config{Packages: []Package{{Name: "hello"}}}
	err := c.validate()
	if err == nil || !strings.Contains(err.Error(), "no RefName") || !strings.Contains(err.Error(), "no Config") {
		t.Errorf("err = %v", err)
	}
}

func TestImagesAddsTheHooksImagesToTheCommands(t *testing.T) {
	cmd, extra := &dagger.Container{}, &dagger.Container{}
	c := Config{Packages: []Package{{Name: "p", Images: []string{"cmd", "extra"}}}}

	got, err := c.images(map[string]*dagger.Container{"cmd": cmd}, map[string]*dagger.Container{"extra": extra})

	if err != nil || len(got) != 2 || got["cmd"] != cmd || got["extra"] != extra {
		t.Errorf("images() = %v, %v", got, err)
	}
}

func TestImagesRejectsAHookImageNamedLikeACommand(t *testing.T) {
	images := map[string]*dagger.Container{"hello": {}}
	_, err := Config{}.images(images, images)
	if err == nil || err.Error() != "Images builds hello, which a command already builds" {
		t.Errorf("err = %v", err)
	}
}

func TestImagesRejectsAPackageImageThatNothingBuilds(t *testing.T) {
	c := Config{Packages: []Package{{Name: "p", Images: []string{"missing"}}}}
	_, err := c.images(nil, nil)
	if err == nil || err.Error() != `package p: nothing builds image "missing"` {
		t.Errorf("err = %v", err)
	}
}

func TestPackageInstallsInstallOnlyManagementPackages(t *testing.T) {
	c := Config{Packages: []Package{
		{Name: "mgmt", RefName: "mgmt.example.com"},
		{Name: "work", RefName: "work.example.com", On: Workload},
	}}

	got, err := c.packageInstalls()

	if err != nil || len(got) != 1 || !strings.Contains(string(got[0]), "name: mgmt\n") {
		t.Errorf("packageInstalls() = %q, %v", got, err)
	}
}

func TestBundlesOnSplitsBundlesByTarget(t *testing.T) {
	c := Config{Packages: []Package{{Name: "mgmt"}, {Name: "work", On: Workload}}}
	bundles := map[string]string{"mgmt": "reg/mgmt@sha256:a", "work": "reg/work@sha256:b"}

	if got := c.bundlesOn(Management, bundles); !slices.Equal(got, []string{"reg/mgmt@sha256:a"}) {
		t.Errorf("bundlesOn(Management) = %v", got)
	}
	if got := c.bundlesOn(Workload, bundles); !slices.Equal(got, []string{"reg/work@sha256:b"}) {
		t.Errorf("bundlesOn(Workload) = %v", got)
	}
}

func TestConfigPathsMustExistUnderRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Config{
		Root:     root,
		Commands: []string{"./cmd/hello", "./cmd/missing"},
		Packages: []Package{{Name: "hello", Config: "config/hello"}},
	}

	err := c.checkPaths()

	for _, want := range []string{"command ./cmd/missing not found under " + root, "package hello's config config/hello not found under " + root} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "./cmd/hello ") {
		t.Errorf("err = %v reports an existing command", err)
	}
}

func TestUpChecksPathsBeforeStarting(t *testing.T) {
	t.Setenv("_EXPERIMENTAL_DAGGER_RUNNER_HOST", "unix:///nonexistent/devenv.sock")
	c := Config{
		Root:     t.TempDir(),
		Commands: []string{"./cmd/hello"},
		Packages: []Package{{Name: "hello", RefName: "hello.example.com", Config: "config/hello"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Up(ctx, c, Options{Name: "paths", StateDir: t.TempDir(), Progress: io.Discard})

	if err == nil || !strings.HasPrefix(err.Error(), "config: command ./cmd/hello not found") {
		t.Errorf("err = %v", err)
	}
}
