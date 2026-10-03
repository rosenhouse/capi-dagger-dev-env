// Command probe exercises devenv's build side the way a polyglot consumer's Images hook would.
// Each case prints OBS lines. It needs a Dagger engine but no Kind.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
)

var (
	ctx   = context.Background()
	c     *dagger.Client
	base  string
	crane *dagger.Container
)

func main() {
	base = os.Args[1]
	logf, _ := os.Create(filepath.Join(base, "probe-dagger.log"))
	var err error
	if c, err = dagger.Connect(ctx, dagger.WithLogOutput(logf)); err != nil {
		panic(err)
	}
	defer c.Close()
	reg := c.Container().From("registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8").WithExposedPort(5000).AsService()
	crane = c.Container().From("gcr.io/go-containerregistry/crane:debug@sha256:e78770b31258a3846f878036d9c1f63fbe4c871f9f56990bf77fd95c013e3c1b").WithServiceBinding("reg", reg)

	cases := []struct {
		name string
		run  func()
	}{
		{"failing RUN", failingRun},
		{"COPY git-ignored file", copyIgnored},
		{"syntax directive and newer Dockerfile features", syntaxDirective},
		{"build secret", buildSecret},
		{"platform args", platformArgs},
		{"invalid image name", invalidName},
		{"snapshot contents", snapshotContents},
		{"version after touch", versionAfterTouch},
		{"go binaries after a python-only edit", binariesAfterPythonEdit},
		{"go module in a subdirectory", nestedGoModule},
		{"relative replace outside Root", relativeReplace},
		{"cgo command", cgoCommand},
		{"sequential pushes", sequentialPushes},
	}
	for _, tc := range cases {
		fmt.Printf("\n===== %s\n", tc.name)
		start := time.Now()
		tc.run()
		fmt.Printf("(%s took %.1fs)\n", tc.name, time.Since(start).Seconds())
	}
}

func obs(format string, args ...any) { fmt.Printf("OBS: "+format+"\n", args...) }

// push mirrors infra.Registry.Push, and wraps errors as devenv's publishPackages and stage do.
func push(ctr *dagger.Container, repo string) (string, error) {
	out, err := crane.
		WithMountedFile("/image.tar", ctr.AsTarball()).
		WithExec([]string{"crane", "push", "--insecure", "/image.tar", "reg:5000/" + repo}).
		Stdout(ctx)
	if execErr := (*dagger.ExecError)(nil); errors.As(err, &execErr) {
		lines := strings.Split(strings.TrimSpace(execErr.Stderr), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		err = fmt.Errorf("%w\n%s", err, strings.Join(lines, "\n"))
	}
	if err != nil {
		return "", fmt.Errorf("stage %q: %w", "images and bundles", fmt.Errorf("push %s: %w", repo, err))
	}
	return strings.TrimSpace(out), nil
}

// repo writes files into a new Git repository under base and commits them.
func repo(name string, files map[string]string) string {
	dir := filepath.Join(base, name)
	for path, content := range files {
		p := filepath.Join(dir, path)
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, []byte(content), 0o644))
	}
	sh(dir, "git init -q && git add -A && git -c user.email=x@x -c user.name=x commit -qm init")
	return dir
}

func sh(dir, script string) string {
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("sh %q: %v\n%s\n", script, err, out)
	}
	return string(out)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func userError(err error) string {
	return "\n----- user would see:\nError: " + err.Error() + "\n-----"
}

func failingRun() {
	dir := repo("t-run", map[string]string{
		"svc/Dockerfile": "FROM python:3.12-slim\nRUN echo step-one-output && python -c \"import flask_not_installed\"\n",
	})
	_, err := push(build.Source(c, dir).Directory("svc").DockerBuild(), "greeter")
	obs("failing RUN: error mentions ModuleNotFoundError=%v, mentions the RUN command=%v, length=%d", strings.Contains(fmt.Sprint(err), "ModuleNotFoundError"), strings.Contains(fmt.Sprint(err), "flask_not_installed"), len(fmt.Sprint(err)))
	fmt.Println(userError(err))
}

func copyIgnored() {
	dir := repo("t-ignored", map[string]string{
		".gitignore":               "generated/\n",
		"svc/Dockerfile":           "FROM python:3.12-slim\nCOPY generated/api_pb2.py /app/\n",
		"svc/generated/api_pb2.py": "STUB = 1\n",
		"svc/vendor/dep.whl":       "wheel\n",
		"svc/Dockerfile.tracked":   "FROM python:3.12-slim\nCOPY vendor/dep.whl /app/\n",
	})
	_, err := push(build.Source(c, dir).Directory("svc").DockerBuild(), "greeter")
	obs("COPY of git-ignored file: err=%v", err != nil)
	if err != nil {
		fmt.Println(userError(err))
	}
	// A file Git tracks although .gitignore matches it.
	must(os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("generated/\n*.whl\n"), 0o644))
	sh(dir, "git add -f svc/vendor/dep.whl .gitignore && git -c user.email=x@x -c user.name=x commit -qm ignore-whl && git ls-files svc/vendor")
	_, err = push(build.Source(c, dir).Directory("svc").DockerBuild(dagger.DirectoryDockerBuildOpts{Dockerfile: "Dockerfile.tracked"}), "greeter")
	obs("COPY of a Git-tracked file that .gitignore matches: err=%v", err != nil)
	if err != nil {
		fmt.Println(userError(err))
	}
}

func syntaxDirective() {
	dir := repo("t-syntax", map[string]string{
		"svc/Dockerfile.heredoc": "# syntax=docker/dockerfile:1\nFROM alpine:3.20\nRUN <<EOF\necho heredoc-ok > /h\nEOF\nRUN --mount=type=cache,target=/root/.cache echo cache-mount-ok > /c\nCMD cat /h /c\n",
		"svc/Dockerfile.exclude": "# syntax=docker/dockerfile:1.7-labs\nFROM alpine:3.20\nCOPY --exclude=*.md . /app/\nRUN ls /app\n",
		"svc/Dockerfile.parents": "# syntax=docker/dockerfile:1.7-labs\nFROM alpine:3.20\nCOPY --parents ./a/x.txt /app/\nRUN ls -R /app\n",
		"svc/README.md":          "doc\n",
		"svc/a/x.txt":            "x\n",
		"svc/Dockerfile.badsyn":  "# syntax=example.invalid/not-a-frontend:1\nFROM alpine:3.20\n",
	})
	src := build.Source(c, dir).Directory("svc")
	for _, df := range []string{"Dockerfile.heredoc", "Dockerfile.exclude", "Dockerfile.parents", "Dockerfile.badsyn"} {
		out, err := src.DockerBuild(dagger.DirectoryDockerBuildOpts{Dockerfile: df}).
			WithExec([]string{"sh", "-c", "cat /h /c 2>/dev/null; ls /app 2>/dev/null; true"}).Stdout(ctx)
		obs("%s: err=%v out=%q", df, err, strings.TrimSpace(out))
	}
}

func buildSecret() {
	dir := repo("t-secret", map[string]string{
		"svc/Dockerfile": "FROM alpine:3.20\nRUN --mount=type=secret,id=pip-token test -s /run/secrets/pip-token && echo secret-present > /s || echo secret-missing > /s\n",
	})
	secret := c.SetSecret("pip-token", "s3cr3t")
	out, err := build.Source(c, dir).Directory("svc").DockerBuild(dagger.DirectoryDockerBuildOpts{Secrets: []*dagger.Secret{secret}}).
		WithExec([]string{"cat", "/s"}).Stdout(ctx)
	obs("secret named pip-token, mounted with id=pip-token: err=%v out=%q", err, strings.TrimSpace(out))
}

func platformArgs() {
	dir := repo("t-platform", map[string]string{
		"svc/Dockerfile": "FROM --platform=$BUILDPLATFORM alpine:3.20 AS b\nARG TARGETARCH\nARG TARGETPLATFORM\nARG BUILDPLATFORM\nRUN echo \"TARGETARCH=$TARGETARCH TARGETPLATFORM=$TARGETPLATFORM BUILDPLATFORM=$BUILDPLATFORM\" > /info\nFROM alpine:3.20\nCOPY --from=b /info /info\n",
	})
	out, err := build.Source(c, dir).Directory("svc").DockerBuild().WithExec([]string{"sh", "-c", "cat /info; uname -m"}).Stdout(ctx)
	obs("platform args: err=%v out=%q", err, strings.TrimSpace(out))
	p, err := c.DefaultPlatform(ctx)
	obs("engine default platform: %s %v", p, err)
}

func invalidName() {
	_, err := push(c.Container().From("alpine:3.20"), "PyGreeter")
	obs("hook image named PyGreeter: err=%v", err != nil)
	if err != nil {
		fmt.Println(userError(err))
	}
}

func snapshotContents() {
	dir := repo("t-snap", map[string]string{
		".gitignore":                             "*.whl\n",
		"vendor/dep.whl":                         "wheel\n",
		"tools/devenv/.devenv/x/mgmt.kubeconfig": "secret\n",
		".devenv/y/mgmt.kubeconfig":              "secret\n",
		"svc/app.py":                             "print(1)\n",
	})
	sh(dir, "git add -f vendor/dep.whl && git -c user.email=x@x -c user.name=x commit -qm whl")
	entries, err := build.Source(c, dir).Glob(ctx, "**")
	obs("snapshot entries: %v (err %v)", entries, err)
	obs("git ls-files: %s", strings.ReplaceAll(sh(dir, "git ls-files"), "\n", " "))
}

func versionAfterTouch() {
	dir := repo("t-touch", map[string]string{"svc/app.py": "print(1)\n", "go.mod": "module x\n"})
	v1, _ := build.Version(ctx, build.Source(c, dir))
	time.Sleep(1100 * time.Millisecond)
	sh(dir, "touch svc/app.py")
	v2, _ := build.Version(ctx, build.Source(c, dir))
	sh(dir, "chmod +x svc/app.py")
	v3, _ := build.Version(ctx, build.Source(c, dir))
	obs("version: initial %s, after touch %s, after chmod +x %s", v1, v2, v3)
}

func binariesAfterPythonEdit() {
	dir := repo("t-gopy", map[string]string{
		"go.mod":              "module example.com/gopy\n\ngo 1.26.1\n",
		"cmd/manager/main.go": "package main\n\nvar version string\n\nfunc main() { println(version) }\n",
		"svc/app.py":          "print(1)\n",
	})
	spec := build.Spec{Root: dir, Commands: []string{"./cmd/manager"}}
	digest := func() (string, string) {
		b, err := build.FromHost(ctx, c, spec, "")
		if err != nil {
			return "", err.Error()
		}
		d, err := b.Binaries.File("manager").Digest(ctx)
		if err != nil {
			return b.Version, err.Error()
		}
		return b.Version, d
	}
	v1, d1 := digest()
	must(os.WriteFile(filepath.Join(dir, "svc/app.py"), []byte("print(2)\n"), 0o644))
	v2, d2 := digest()
	obs("go manager binary after a python-only edit: version %s -> %s, binary digest %s -> %s, binary changed=%v", v1, v2, d1, d2, d1 != d2)
}

func nestedGoModule() {
	dir := repo("t-nested", map[string]string{
		"controller/go.mod":              "module example.com/controller\n\ngo 1.26.1\n",
		"controller/cmd/manager/main.go": "package main\n\nvar version string\n\nfunc main() { println(version) }\n",
		"services/py/app.py":             "print(1)\n",
	})
	try := func(label string) {
		b, err := build.FromHost(ctx, c, build.Spec{Root: dir, Commands: []string{"./controller/cmd/manager"}}, "v1")
		if err == nil {
			_, err = push(build.Images(c, b.Binaries, []string{"./controller/cmd/manager"})["manager"], "manager")
		}
		obs("%s: err=%v", label, err != nil)
		if err != nil {
			fmt.Println(userError(err))
		}
	}
	try("Root without go.mod, Commands ./controller/cmd/manager")
	must(os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.26.1\n\nuse ./controller\n"), 0o644))
	try("same, with a go.work at Root that uses ./controller")
}

func relativeReplace() {
	dir := repo("t-replace", map[string]string{
		"api/go.mod":                     "module example.com/api\n\ngo 1.26.1\n",
		"api/api.go":                     "package api\n\nconst Name = \"api\"\n",
		"controller/go.mod":              "module example.com/controller\n\ngo 1.26.1\n\nrequire example.com/api v0.0.0\n\nreplace example.com/api => ../api\n",
		"controller/cmd/manager/main.go": "package main\n\nimport \"example.com/api\"\n\nvar version string\n\nfunc main() { println(api.Name, version) }\n",
	})
	root := filepath.Join(dir, "controller")
	out := sh(root, "go build ./cmd/manager && echo host-build-ok")
	obs("host go build: %s", strings.TrimSpace(out))
	b, err := build.FromHost(ctx, c, build.Spec{Root: root, Commands: []string{"./cmd/manager"}}, "v1")
	if err == nil {
		_, err = push(build.Images(c, b.Binaries, []string{"./cmd/manager"})["manager"], "manager")
	}
	obs("Root=controller with replace => ../api: err=%v", err != nil)
	if err != nil {
		fmt.Println(userError(err))
	}
}

func cgoCommand() {
	dir := repo("t-cgo", map[string]string{
		"go.mod":              "module example.com/cgo\n\ngo 1.26.1\n",
		"cmd/sqlite/main.go":  "package main\n\n/*\n#include <stdio.h>\nstatic void hi() { printf(\"hi\\n\"); }\n*/\nimport \"C\"\n\nvar version string\n\nfunc main() { C.hi() }\n",
		"cmd/manager/main.go": "package main\n\nvar version string\n\nfunc main() {}\n",
	})
	cmds := []string{"./cmd/manager", "./cmd/sqlite"}
	b, err := build.FromHost(ctx, c, build.Spec{Root: dir, Commands: cmds}, "v1")
	if err == nil {
		_, err = push(build.Images(c, b.Binaries, cmds)["sqlite"], "sqlite")
	}
	obs("cgo-only command: err=%v", err != nil)
	if err != nil {
		fmt.Println(userError(err))
	}
}

// sequentialPushes times three images that each take 20s, pushed one after another as publishPackages does, then at once.
func sequentialPushes() {
	images := func(nonce string) map[string]*dagger.Container {
		m := map[string]*dagger.Container{}
		for _, n := range []string{"a", "b", "c"} {
			m[n] = c.Container().From("alpine:3.20").WithEnvVariable("NONCE", nonce+n).WithExec([]string{"sleep", "20"})
		}
		return m
	}
	start := time.Now()
	for name, img := range images(fmt.Sprint("seq", time.Now().UnixNano())) {
		if _, err := push(img, name); err != nil {
			obs("push %s: %v", name, err)
		}
	}
	seq := time.Since(start)
	start = time.Now()
	var wg sync.WaitGroup
	for name, img := range images(fmt.Sprint("par", time.Now().UnixNano())) {
		wg.Go(func() {
			if _, err := push(img, name); err != nil {
				obs("push %s: %v", name, err)
			}
		})
	}
	wg.Wait()
	obs("three 20s image builds: pushed in sequence %.1fs, pushed concurrently %.1fs", seq.Seconds(), time.Since(start).Seconds())
}
