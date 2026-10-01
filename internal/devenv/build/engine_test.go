package build_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/build"
)

// TestSessionSeesHostEdits needs a Dagger engine, so it runs only with DEVENV_ENGINE_TESTS set.
func TestSessionSeesHostEdits(t *testing.T) {
	if os.Getenv("DEVENV_ENGINE_TESTS") == "" {
		t.Skip("set DEVENV_ENGINE_TESTS to run against a Dagger engine")
	}
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	root := t.TempDir()
	write(t, root, "go.mod", "module example\n")
	write(t, root, "cmd/hello/main.go", "package main\n")
	write(t, root, "config/hello/a.yaml", "a: 1\n")
	srcBefore, configBefore := versions(t, ctx, c, root)

	write(t, root, "cmd/hello/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "config/hello/a.yaml", "a: 2\n")
	srcAfter, configAfter := versions(t, ctx, c, root)

	if srcAfter == srcBefore {
		t.Errorf("Version(Source) stayed %s after an edit", srcBefore)
	}
	if configAfter == configBefore {
		t.Errorf("Version(Config) stayed %s after an edit", configBefore)
	}
}

func versions(t *testing.T, ctx context.Context, c *dagger.Client, root string) (src, config string) {
	t.Helper()
	src, err := build.Version(ctx, build.Source(c, root))
	if err != nil {
		t.Fatal(err)
	}
	config, err = build.Version(ctx, build.Config(c, root))
	if err != nil {
		t.Fatal(err)
	}
	return src, config
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
