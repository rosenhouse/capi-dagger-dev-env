package hostbuild_test

import (
	"bytes"
	"cmp"
	"context"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestBuildCompilesEachCommandForLinuxIgnoringTheHostsSettings(t *testing.T) {
	for key, value := range map[string]string{"GOOS": "windows", "CGO_ENABLED": "1", "GOFLAGS": "-tags=hostonly",
		"GOEXPERIMENT": "fieldtrack", "GOAMD64": "v3", "GOARM64": "v9.0"} {
		t.Setenv(key, value)
	}
	for _, c := range []struct {
		arch, level, levelDefault string
		machine                   elf.Machine
	}{{"amd64", "GOAMD64", "v1", elf.EM_X86_64}, {"arm64", "GOARM64", "v8.0", elf.EM_AARCH64}} {
		t.Run(c.arch, func(t *testing.T) {
			root, out := module(t), t.TempDir()
			gitInit(t, root)

			if _, err := hostbuild.Build(context.Background(), root, c.arch, "v1.2.3", out); err != nil {
				t.Fatal(err)
			}

			for _, name := range hostbuild.Commands {
				path := filepath.Join(out, name)
				if got := elfMachine(t, path); got != c.machine {
					t.Errorf("%s is for %v, want %v", name, got, c.machine)
				}
				want := map[string]string{"GOOS": "linux", "GOARCH": c.arch, "CGO_ENABLED": "0", "-trimpath": "true", c.level: c.levelDefault}
				got := settings(t, path)
				_, tags := got["-tags"]
				_, experiment := got["GOEXPERIMENT"]
				if !mapContains(got, want) || hasVCS(got) || tags || experiment {
					t.Errorf("%s build settings %v, want %v and no vcs, tags or experiment", name, got, want)
				}
			}
		})
	}
}

func TestBuildTakesRelativePathsFromTheWorkingDirectory(t *testing.T) {
	root := module(t)
	wd := filepath.Dir(root)
	t.Chdir(wd)

	if _, err := hostbuild.Build(context.Background(), filepath.Base(root), runtime.GOARCH, "v1", "out"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(wd, "out", "hello")); err != nil {
		t.Error(err)
	}
}

func TestBuildWorksThroughASymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(module(t), link); err != nil {
		t.Fatal(err)
	}

	if _, err := hostbuild.Build(context.Background(), link, runtime.GOARCH, "v1", t.TempDir()); err != nil {
		t.Error(err)
	}
}

func TestBuildStampsTheVersion(t *testing.T) {
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
			// -trimpath keeps -ldflags out of the build settings.
			if b, err := os.ReadFile(filepath.Join(out, "hello")); err != nil || !bytes.Contains(b, []byte(want)) {
				t.Errorf("hello does not hold %q: %v", want, err)
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

func TestBuildSnapshotsEachPackagesConfigWithTheSource(t *testing.T) {
	root := module(t)
	write(t, root, "config/hello/sub/b.yaml", "b: 1\n")
	write(t, root, "config/other/c.yaml", "c: 1\n")
	write(t, root, "config/notes.md", "in no package\n")
	editDuringBuild(t, filepath.Join(root, "config/hello/a.yaml"))

	snap, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]map[string]string{
		"hello": {"a.yaml": "a: 1\n", "sub/b.yaml": "b: 1\n"},
		"other": {"c.yaml": "c: 1\n"},
	}
	got := map[string]map[string]string{}
	for pkg, files := range snap.Config {
		got[pkg] = map[string]string{}
		for path, content := range files {
			got[pkg][path] = string(content)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Config = %v, want %v", got, want)
	}
}

func TestBuildAcceptsImportsFromTheVersionedSourceAndTheModuleCache(t *testing.T) {
	root := module(t)
	write(t, root, "go.mod", "module example\n\ngo 1.21\n\nrequire github.com/google/go-cmp v0.7.0\n")
	write(t, root, "go.sum", goCmpSum)
	write(t, root, "internal/greet/greet.go", "package greet\n\nconst Hi = \"hi\"\n")
	write(t, root, "cmd/hello/main.go", importing("example/internal/greet", "github.com/google/go-cmp/cmp"))

	if _, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir()); err != nil {
		t.Error(err)
	}
}

func TestBuildRejectsImportsThatVersionLeavesOut(t *testing.T) {
	for _, c := range []struct {
		files             map[string]string
		importPath, inDir string
	}{
		{map[string]string{"pkg/greet/greet.go": "package greet\n"}, "example/pkg/greet", "pkg/greet"},
		{map[string]string{"internal/devenv/greet/greet.go": "package greet\n"}, "example/internal/devenv/greet", "internal/devenv/greet"},
		{map[string]string{
			"go.mod":                            "module example\n\ngo 1.21\n\nrequire example.com/greet v1.0.0\n",
			"vendor/modules.txt":                "# example.com/greet v1.0.0\n## explicit\nexample.com/greet\n",
			"vendor/example.com/greet/greet.go": "package greet\n",
		}, "example.com/greet", "vendor/example.com/greet"},
		{map[string]string{
			"go.mod":                  "module example\n\ngo 1.21\n\nrequire example.com/greet v1.0.0\n\nreplace example.com/greet => ../replacement\n",
			"../replacement/go.mod":   "module example.com/greet\n",
			"../replacement/greet.go": "package greet\n",
		}, "example.com/greet", "replacement"},
	} {
		t.Run(c.inDir, func(t *testing.T) {
			root := module(t)
			for path, content := range c.files {
				write(t, root, path, content)
			}
			write(t, root, "cmd/hello/main.go", importing(c.importPath))

			_, err := hostbuild.Build(context.Background(), root, runtime.GOARCH, "v1", t.TempDir())

			if err == nil || !strings.Contains(err.Error(), c.inDir) {
				t.Errorf("err = %v, want one naming %s", err, c.inDir)
			}
		})
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

// goCmpSum is go.sum for github.com/google/go-cmp v0.7.0.
const goCmpSum = `github.com/google/go-cmp v0.7.0 h1:wk8382ETsv4JYUZwIsn6YpYiWiBsYLSJiTsyBybVuN8=
github.com/google/go-cmp v0.7.0/go.mod h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=
`

// importing is a main package that imports each of importPaths for its side effects and prints version.
func importing(importPaths ...string) string {
	var b strings.Builder
	b.WriteString("package main\n\n")
	for _, p := range importPaths {
		fmt.Fprintf(&b, "import _ %q\n", p)
	}
	b.WriteString("\nvar version string\n\nfunc main() { println(version) }\n")
	return b.String()
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
