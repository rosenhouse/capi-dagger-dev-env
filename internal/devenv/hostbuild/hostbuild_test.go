package hostbuild_test

import (
	"cmp"
	"context"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/hostbuild"
)

func TestModuleRootFindsNearestGoModAbove(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example\n")
	nested := filepath.Join(root, "internal", "devenv")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := hostbuild.ModuleRoot(nested)

	if err != nil || got != root {
		t.Errorf("ModuleRoot() = %q, %v; want %q", got, err, root)
	}
}

func TestModuleRootFailsOutsideAModule(t *testing.T) {
	if _, err := hostbuild.ModuleRoot(t.TempDir()); err == nil {
		t.Error("no error")
	}
}

func TestCommandsAreTheModulesCommandsButDevenv(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := hostbuild.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "devenv" {
			want = append(want, e.Name())
		}
	}

	if got := slices.Sorted(slices.Values(hostbuild.Commands)); !slices.Equal(got, want) {
		t.Errorf("Commands = %v, want %v", got, want)
	}
}

func TestVersionNamesContentNotLocation(t *testing.T) {
	first, second := module(t), module(t)

	v1, v2 := version(t, first), version(t, second)

	if v1 != v2 {
		t.Errorf("equal source in two places has versions %s and %s", v1, v2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v1) {
		t.Errorf("version %q is not 12 hex digits", v1)
	}
}

func TestVersionChangesWithTheFirstPartySource(t *testing.T) {
	for _, path := range []string{
		"go.mod",
		"go.sum",
		"api/v1/types.go",
		"cmd/hello/main.go",
		"cmd/hello/testdata/x.txt",
		"internal/greeting/greeting.go",
		"internal/devenvtools/x.go",
	} {
		t.Run(path, func(t *testing.T) {
			root := module(t)
			write(t, root, path, "one\n")
			before := version(t, root)

			write(t, root, path, "two\n")

			if after := version(t, root); after == before {
				t.Errorf("version stayed %s", before)
			}
		})
	}
}

func TestVersionIgnoresTheOrchestratorTestsAndConfig(t *testing.T) {
	for _, path := range []string{
		"cmd/devenv/main.go",
		"internal/devenv/devenv.go",
		"cmd/hello/main_test.go",
		"internal/greeting/greeting_test.go",
		"config/hello/a.yaml",
		"README.md",
		"go.work",
	} {
		t.Run(path, func(t *testing.T) {
			root := module(t)
			before := version(t, root)

			write(t, root, path, "edited\n")

			if after := version(t, root); after != before {
				t.Errorf("version changed from %s to %s", before, after)
			}
		})
	}
}

func TestVersionChangesWhenAFileMoves(t *testing.T) {
	root := module(t)
	write(t, root, "internal/a/x.go", "package a\n")
	before := version(t, root)

	if err := os.Rename(filepath.Join(root, "internal/a"), filepath.Join(root, "internal/b")); err != nil {
		t.Fatal(err)
	}

	if after := version(t, root); after == before {
		t.Errorf("version stayed %s", before)
	}
}

func TestVersionSeparatesFiles(t *testing.T) {
	split, joined := module(t), module(t)
	write(t, split, "cmd/x/a.go", "a")
	write(t, split, "cmd/x/b.go", "b")
	write(t, joined, "cmd/x/a.go", "acmd/x/b.go\x00b")

	if version(t, split) == version(t, joined) {
		t.Error("one file holding another's path and content has the same version as the two files")
	}
}

func TestBuildCompilesEachCommandForLinux(t *testing.T) {
	t.Setenv("GOOS", "windows")
	t.Setenv("CGO_ENABLED", "1")
	for arch, machine := range map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64} {
		t.Run(arch, func(t *testing.T) {
			root, out := module(t), t.TempDir()
			gitInit(t, root)

			if _, err := hostbuild.Build(context.Background(), root, arch, "v1.2.3", out); err != nil {
				t.Fatal(err)
			}

			for _, name := range hostbuild.Commands {
				path := filepath.Join(out, name)
				if got := elfMachine(t, path); got != machine {
					t.Errorf("%s is for %v, want %v", name, got, machine)
				}
				want := map[string]string{"GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0", "-trimpath": "true"}
				if got := settings(t, path); !mapContains(got, want) || hasVCS(got) {
					t.Errorf("%s build settings %v, want %v and no vcs", name, got, want)
				}
			}
		})
	}
}

func TestBuildStampsTheVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("runs linux binaries")
	}
	for _, stamp := range []string{"v1.2.3", ""} {
		t.Run(stamp, func(t *testing.T) {
			root, out := module(t), t.TempDir()
			want := cmp.Or(stamp, version(t, root))

			snap, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, stamp, out)
			if err != nil {
				t.Fatal(err)
			}

			if snap.Version != version(t, root) {
				t.Errorf("snapshot version %s, want %s", snap.Version, version(t, root))
			}
			got, err := exec.Command(filepath.Join(out, "hello")).CombinedOutput()
			if err != nil || string(got) != want+"\n" {
				t.Errorf("hello printed %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestBuildIgnoresWorkspaces(t *testing.T) {
	root := module(t)
	write(t, root, "go.work", "go 1.21\n\nuse ./other\n")
	write(t, root, "other/go.mod", "module other\n")

	if _, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir()); err != nil {
		t.Error(err)
	}
}

func TestBuildSnapshotsConfigWithTheSource(t *testing.T) {
	root := module(t)
	write(t, root, "config/hello/sub/b.yaml", "b: 1\n")
	editDuringBuild(t, filepath.Join(root, "config/hello/a.yaml"))

	snap, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"hello/a.yaml": "a: 1\n", "hello/sub/b.yaml": "b: 1\n"}
	got := map[string]string{}
	for path, content := range snap.Config {
		got[path] = string(content)
	}
	if !maps.Equal(got, want) {
		t.Errorf("Config = %v, want %v", got, want)
	}
}

func TestBuildFailsWhenTheSourceChangesDuringIt(t *testing.T) {
	root := module(t)
	editDuringBuild(t, filepath.Join(root, "cmd/hello/extra.go"))

	_, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("err = %v, want source changed", err)
	}
}

func TestBuildReportsCompilerErrors(t *testing.T) {
	root := module(t)
	write(t, root, "cmd/hello/main.go", "package main\n\nfunc main() { undefinedThing() }\n")

	_, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "undefinedThing") {
		t.Errorf("err = %v, want the compiler's message", err)
	}
}

// module writes a Go module with a main package for each command, and config for one package.
func module(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example\n")
	for _, name := range hostbuild.Commands {
		write(t, root, "cmd/"+name+"/main.go", "package main\n\nvar version string\n\nfunc main() { println(version) }\n")
	}
	write(t, root, "config/hello/a.yaml", "a: 1\n")
	return root
}

// editDuringBuild puts a go command on PATH that writes to path before running the real go.
func editDuringBuild(t *testing.T, path string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho 'package main' > '%s'\nexec '%s' \"$@\"\n", path, goBin)
	write(t, dir, "go", script)
	if err := os.Chmod(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func version(t *testing.T, root string) string {
	t.Helper()
	v, err := hostbuild.Version(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func elfMachine(t *testing.T, path string) elf.Machine {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return f.Machine
}

func settings(t *testing.T, path string) map[string]string {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := map[string]string{}
	for _, setting := range info.Settings {
		s[setting.Key] = setting.Value
	}
	return s
}

func hasVCS(settings map[string]string) bool {
	for key := range settings {
		if strings.HasPrefix(key, "vcs") {
			return true
		}
	}
	return false
}

func mapContains(m, sub map[string]string) bool {
	for k, v := range sub {
		if got, ok := m[k]; !ok || got != v {
			return false
		}
	}
	return true
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
