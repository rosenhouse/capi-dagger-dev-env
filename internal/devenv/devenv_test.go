package devenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// TestMain lets the test binary stand in for smolvm when FAKE_SMOLVM_DIR is set.
func TestMain(m *testing.M) {
	if dir := os.Getenv("FAKE_SMOLVM_DIR"); dir != "" {
		os.Exit(fakeSmolvmMain(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeSmolvmMain appends each call to dir/calls and keeps the machines in dir/machines.json.
// It prints FAKE_SMOLVM_VERSION for --version, and fails calls that start with FAKE_SMOLVM_FAIL.
func fakeSmolvmMain(dir string, args []string) int {
	call := strings.Join(args, " ")
	f, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(f, call)
	f.Close()
	if fail := os.Getenv("FAKE_SMOLVM_FAIL"); fail != "" && strings.HasPrefix(call, fail) {
		fmt.Fprintln(os.Stderr, "fake failure")
		return 1
	}
	machines := readMachines(dir)
	name := ""
	if i := slices.Index(args, "--name"); i >= 0 && i+1 < len(args) {
		name = args[i+1]
	}
	named := func(m smolvm.Machine) bool { return m.Name == name }
	switch {
	case call == "--version":
		fmt.Println(os.Getenv("FAKE_SMOLVM_VERSION"))
	case call == "machine ls --json":
		data, _ := json.Marshal(machines)
		fmt.Println(string(data))
	case strings.HasPrefix(call, "machine create "):
		machines = append(machines, smolvm.Machine{Name: name, State: smolvm.Created})
	case strings.HasPrefix(call, "machine start "):
		machines[slices.IndexFunc(machines, named)].State = smolvm.Running
	case strings.HasPrefix(call, "machine delete "):
		machines = slices.DeleteFunc(machines, named)
	}
	writeMachines(dir, machines)
	return 0
}

func readMachines(dir string) []smolvm.Machine {
	var machines []smolvm.Machine
	data, err := os.ReadFile(filepath.Join(dir, "machines.json"))
	if err == nil {
		err = json.Unmarshal(data, &machines)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		panic(err)
	}
	return machines
}

func writeMachines(dir string, machines []smolvm.Machine) {
	data, err := json.Marshal(machines)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "machines.json"), data, 0o600); err != nil {
		panic(err)
	}
}

// fake is a fake smolvm and an environment called alpha.
type fake struct {
	o   Options
	env state.Env
	dir string
}

// fakeSmolvm starts a fake smolvm whose machines are those that machines returns for alpha.
func fakeSmolvm(t *testing.T, machines func(state.Env) []smolvm.Machine) fake {
	t.Helper()
	f := fake{o: Options{Name: "alpha", StateDir: t.TempDir(), SmolVM: smolvm.CLI{Path: os.Args[0]}}, dir: t.TempDir()}
	var err error
	if f.env, err = state.New(f.o.StateDir, f.o.Name); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMachines(f.dir, machines(f.env))
	t.Setenv("FAKE_SMOLVM_DIR", f.dir)
	t.Setenv("FAKE_SMOLVM_VERSION", "smolvm "+smolvm.Version)
	return f
}

// calls returns smolvm's calls so far.
func (f fake) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (f fake) machines() []smolvm.Machine { return readMachines(f.dir) }

func vm(s smolvm.State) func(state.Env) []smolvm.Machine {
	return func(env state.Env) []smolvm.Machine {
		if s == "" {
			return nil
		}
		return []smolvm.Machine{{Name: env.VM(), State: s}}
	}
}

// fakeHost stands in for /dev/kvm and fills the download cache, so Up needs neither KVM nor the network.
func fakeHost(t *testing.T) {
	t.Helper()
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kvmDevice = device
	t.Cleanup(func() { kvmDevice = "/dev/kvm" })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	downloads, err := infra.Downloads(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(downloads, platform.Downloads()...) {
		path := filepath.Join(cacheDir, "devenv", "downloads", "sha256", d.SHA256)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeKubeconfigs(t *testing.T, env state.Env) {
	t.Helper()
	for _, name := range []string{"mgmt.kubeconfig", "workload.kubeconfig", "ports.json"} {
		if err := os.WriteFile(filepath.Join(env.Dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpRefusesAnEnvironmentWhoseVMIsRunning(t *testing.T) {
	fakeHost(t)
	f := fakeSmolvm(t, vm(smolvm.Running))
	env := f.env
	writeKubeconfigs(t, env)
	if err := os.WriteFile(filepath.Join(env.Dir, "guest.log"), []byte("kind create cluster\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Up(t.Context(), f.o)

	if err == nil || !strings.Contains(err.Error(), "environment alpha is already up") {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"--version", "machine ls --json"}) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "mgmt.kubeconfig")); err != nil {
		t.Errorf("kubeconfig of the running environment: %v", err)
	}
	if log, err := os.ReadFile(filepath.Join(env.Dir, "guest.log")); err != nil || string(log) != "kind create cluster\n" {
		t.Errorf("guest.log of the running environment = %q, %v", log, err)
	}
}

func TestUpDeletesTheVMWhenBringUpFails(t *testing.T) {
	fakeHost(t)
	f := fakeSmolvm(t, vm(""))
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")

	_, err := Up(t.Context(), f.o)

	if err == nil || !strings.Contains(err.Error(), `stage "VM"`) || !strings.Contains(err.Error(), "logs: "+f.env.Dir) {
		t.Errorf("err = %v", err)
	}
	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after a failed up: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.env.Dir, "ports.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ports.json: %v", err)
	}
}

func TestUpReplacesALeftoverVMAndForgetsTheLastRun(t *testing.T) {
	fakeHost(t)
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	writeKubeconfigs(t, f.env)
	if err := os.MkdirAll(filepath.Join(f.env.Dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env.Dir, "logs", "last-run.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")

	if _, err := Up(t.Context(), f.o); err == nil {
		t.Fatal("no error")
	}

	got := f.calls(t)
	deleted, created := slices.Index(got, "machine delete --name "+f.env.VM()+" -f"), slices.IndexFunc(got, func(c string) bool {
		return strings.HasPrefix(c, "machine create --name "+f.env.VM())
	})
	if deleted < 0 || created < deleted {
		t.Errorf("smolvm calls = %q; want the leftover deleted before the create", got)
	}
	for _, name := range []string{"mgmt.kubeconfig", "workload.kubeconfig", "logs/last-run.log"} {
		if _, err := os.Stat(filepath.Join(f.env.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExportLogsSkipsAnInterruptedRun(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	var progress bytes.Buffer
	f.o.Progress = &progress
	e, err := open(f.env, f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	e.ExportLogs(ctx)

	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q", got)
	}
	if !strings.Contains(progress.String(), "interrupted") {
		t.Errorf("progress = %q", progress.String())
	}
}

func TestCreateVMWaitsWhileAnotherEnvironmentStartsItsVM(t *testing.T) {
	fakeHost(t)
	f := fakeSmolvm(t, vm(""))
	e, err := open(f.env, f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	unlock, err := state.WaitLock(t.Context(), e.startLock())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	err = e.createVM(ctx, "")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q", got)
	}
}

func TestCreateVMReplacesALeftoverVMAndPublishesTheRecordedPorts(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	env := f.env
	e, err := open(env, f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if err := e.createVM(t.Context(), smolvm.Stopped); err != nil {
		t.Fatal(err)
	}

	p, err := env.ReadPorts()
	if err != nil || p != e.Ports {
		t.Fatalf("recorded ports %+v, %v; want %+v", p, err, e.Ports)
	}
	got := f.calls(t)
	if len(got) != 3 || got[0] != "machine delete --name "+env.VM()+" -f" || !strings.HasPrefix(got[2], "machine start --name "+env.VM()) {
		t.Fatalf("smolvm calls = %q; want delete, create and start", got)
	}
	for _, publish := range []string{fmt.Sprintf("-p %d:6443", p.MgmtAPI), fmt.Sprintf("-p %d:7443", p.WorkloadAPI), fmt.Sprintf("-p %d:5000", p.Registry)} {
		if !strings.Contains(got[1], publish) {
			t.Errorf("%q does not publish %q", got[1], publish)
		}
	}
}

func TestDownDeletesTheVMAndKubeconfigs(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	env := f.env
	writeKubeconfigs(t, env)
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, false, &out); err != nil {
		t.Fatal(err)
	}

	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after down: %+v", got)
	}
	if got := f.calls(t); !slices.Contains(got, "machine delete --name "+env.VM()+" -f") {
		t.Errorf("smolvm calls = %q", got)
	}
	for _, name := range []string{"mgmt.kubeconfig", "workload.kubeconfig", "ports.json"} {
		if _, err := os.Stat(filepath.Join(env.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
	if out.String() != "Deleted the VM of environment alpha.\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestDownPurgesTheStateOfAnEnvironmentWithoutAVM(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	env := f.env
	var out bytes.Buffer

	if err := Down(t.Context(), f.o, true, &out); err != nil {
		t.Fatal(err)
	}

	if got := f.calls(t); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, "delete") }) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(env.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: %v", err)
	}
	if want := "Environment alpha has no VM.\nDeleted " + env.Dir + ".\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestDownFindsANamedEnvironmentWhoseStateDirIsGone(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	if err := os.RemoveAll(f.o.StateDir); err != nil {
		t.Fatal(err)
	}

	if err := Down(t.Context(), f.o, true, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after down: %+v", got)
	}
}

func TestDownChecksSmolvmsVersion(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	env := f.env
	t.Setenv("FAKE_SMOLVM_VERSION", "smolvm 1.21.0")

	err := Down(t.Context(), f.o, true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "want smolvm "+smolvm.Version) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"--version"}) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
}

func TestDownWaitsForNoOtherCommand(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	env := f.env
	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = Down(t.Context(), f.o, true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "another devenv command is using environment alpha") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
}

func TestOpenRequiresARunningVM(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	env := f.env
	writeKubeconfigs(t, env)

	_, err := Open(t.Context(), f.o)

	if err == nil || !strings.Contains(err.Error(), "environment alpha is not running") {
		t.Errorf("err = %v", err)
	}
	if _, err := env.Lock(); err != nil {
		t.Errorf("Open did not release the environment: %v", err)
	}
}

func TestRedeployRejectsVersionsThatLdflagsCannotTake(t *testing.T) {
	e := &Environment{start: time.Now()}
	for _, version := range []string{"it's", "a b"} {
		want := fmt.Sprintf("version %q is not letters, digits and ._+-", version)
		if err := e.Redeploy(t.Context(), version); err == nil || err.Error() != want {
			t.Errorf("%q: err = %v", version, err)
		}
	}
}

func TestCheckKVMExplainsAMissingDevice(t *testing.T) {
	err := checkKVM(filepath.Join(t.TempDir(), "kvm"))

	if err == nil || !strings.Contains(err.Error(), "nested virtualization") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckKVMExplainsADeviceTheUserCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any file")
	}
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o400); err != nil {
		t.Fatal(err)
	}

	if err := checkKVM(device); err == nil || !strings.Contains(err.Error(), "usermod -aG kvm") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckKVMAcceptsADeviceTheUserCanOpen(t *testing.T) {
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkKVM(device); err != nil {
		t.Error(err)
	}
}

func TestStageReportsProgressAndNamesFailures(t *testing.T) {
	var progress bytes.Buffer
	e := &Environment{opts: Options{Progress: &progress}, start: time.Now()}

	if err := e.stage("docker daemon", func() error { return nil }); err != nil {
		t.Errorf("successful stage returned %v", err)
	}
	err := e.stage("management cluster", func() error { return errors.New("boom") })

	if err == nil || err.Error() != `stage "management cluster": boom` {
		t.Errorf("err = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(progress.String()), "\n")
	for i, want := range []string{"] docker daemon", "] docker daemon: 0.0s", "] management cluster"} {
		if i >= len(lines) || !strings.HasSuffix(lines[i], want) {
			t.Errorf("progress line %d of %q does not end with %q", i, lines, want)
		}
	}
	if len(lines) != 3 {
		t.Errorf("progress = %q; want no duration for the failed stage", lines)
	}
}

// Every unpinned image a package's config references must be locked by that package, and vice versa.
func TestPackageImagesMatchConfigPlaceholders(t *testing.T) {
	for _, p := range packages {
		got := placeholders(t, filepath.Join("..", "..", "config", p.name))
		want := slices.Clone(p.images)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: config placeholders %v, package images %v", p.name, got, want)
		}
	}
}

func TestSubsetRejectsMissingKeys(t *testing.T) {
	if _, err := subset(map[string]string{"a": "1"}, []string{"a", "b"}); err == nil {
		t.Error("no error for missing key")
	}
}

// placeholders returns the sorted image values in dir that kbld must resolve: every "image" field,
// and every field named by a kbld searchRule, except digest-pinned references.
func placeholders(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no config in %s: %v", dir, err)
	}
	var docs []map[string]any
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			obj := map[string]any{}
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			docs = append(docs, obj)
		}
	}
	keys := map[string]bool{"image": true}
	for _, doc := range docs {
		if doc["kind"] == "Config" {
			rules, _ := doc["searchRules"].([]any)
			for _, r := range rules {
				name, _ := r.(map[string]any)["keyMatcher"].(map[string]any)["name"].(string)
				keys[name] = true
			}
		}
	}
	found := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if s, ok := child.(string); ok && keys[k] && !strings.Contains(s, "@sha256:") {
					found[s] = true
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	for _, doc := range docs {
		if doc["kind"] != "Config" {
			walk(doc)
		}
	}
	return slices.Sorted(maps.Keys(found))
}
