package build_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
)

func TestModuleRootFindsNearestGoModAbove(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "devenv")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := build.ModuleRoot(nested)

	if err != nil || got != root {
		t.Errorf("ModuleRoot() = %q, %v; want %q", got, err, root)
	}
}

func TestModuleRootFailsOutsideAModule(t *testing.T) {
	if _, err := build.ModuleRoot(t.TempDir()); err == nil {
		t.Error("no error")
	}
}

func TestImageNameIsTheCommandsDirectory(t *testing.T) {
	for _, command := range []string{"./cmd/hello", "./cmd/hello/", "./hello"} {
		if got := build.ImageName(command); got != "hello" {
			t.Errorf("ImageName(%q) = %q", command, got)
		}
	}
}
