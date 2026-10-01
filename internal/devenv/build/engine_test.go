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

// The tests in this file need a Dagger engine, so they run only with DEVENV_ENGINE_TESTS set.

func TestSessionSeesHostEdits(t *testing.T) {
	ctx, c, root := engineAndModule(t)
	srcBefore, configBefore := versions(t, ctx, build.Source(c, root), build.Config(c, root))

	edit(t, root)
	srcAfter, configAfter := versions(t, ctx, build.Source(c, root), build.Config(c, root))

	if srcAfter == srcBefore {
		t.Errorf("Version(Source) stayed %s after an edit", srcBefore)
	}
	if configAfter == configBefore {
		t.Errorf("Version(Config) stayed %s after an edit", configBefore)
	}
}

func TestSnapshotIgnoresLaterHostEdits(t *testing.T) {
	ctx, c, root := engineAndModule(t)
	src, config, err := build.Snapshot(ctx, c, root)
	if err != nil {
		t.Fatal(err)
	}
	srcBefore, configBefore := versions(t, ctx, src, config)

	edit(t, root)
	srcAfter, configAfter := versions(t, ctx, src, config)

	if srcAfter != srcBefore || configAfter != configBefore {
		t.Errorf("snapshot versions changed from %s, %s to %s, %s after an edit", srcBefore, configBefore, srcAfter, configAfter)
	}
}

func TestSecondSessionReusesTheBuild(t *testing.T) {
	requireEngine(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := build.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	builtAt := func() string {
		ctx := context.Background()
		c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, err := build.FromHost(ctx, c, root, "")
		if err != nil {
			t.Fatal(err)
		}
		stamp, err := b.Binaries.File("built-at").Contents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return stamp
	}

	first, second := builtAt(), builtAt()

	if first != second {
		t.Errorf("the second session rebuilt: built at %q, then %q", first, second)
	}
}

func requireEngine(t *testing.T) {
	t.Helper()
	if os.Getenv("DEVENV_ENGINE_TESTS") == "" {
		t.Skip("set DEVENV_ENGINE_TESTS to run against a Dagger engine")
	}
}

func engineAndModule(t *testing.T) (context.Context, *dagger.Client, string) {
	t.Helper()
	requireEngine(t)
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	root := t.TempDir()
	write(t, root, "go.mod", "module example\n")
	write(t, root, "cmd/hello/main.go", "package main\n")
	write(t, root, "config/hello/a.yaml", "a: 1\n")
	return ctx, c, root
}

func edit(t *testing.T, root string) {
	write(t, root, "cmd/hello/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "config/hello/a.yaml", "a: 2\n")
}

func versions(t *testing.T, ctx context.Context, src, config *dagger.Directory) (string, string) {
	t.Helper()
	srcVersion, err := build.Version(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	configVersion, err := build.Version(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return srcVersion, configVersion
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
