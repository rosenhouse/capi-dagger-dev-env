package devenv

import (
	"bytes"
	"cmp"
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
	"syscall"
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
// It prints FAKE_SMOLVM_VERSION for --version, and FAKE_SMOLVM_EXEC_STDOUT for machine exec.
// It fails calls that start with FAKE_SMOLVM_FAIL, printing FAKE_SMOLVM_FAIL_STDERR, or only the first such call
// with FAKE_SMOLVM_FAIL_ONCE set.
// Calls run one at a time, as smolvm's own database serializes them.
func fakeSmolvmMain(dir string, args []string) int {
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		panic(err)
	}
	call := strings.Join(args, " ")
	f, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(f, call)
	f.Close()
	if fail := os.Getenv("FAKE_SMOLVM_FAIL"); fail != "" && strings.HasPrefix(call, fail) {
		failed := filepath.Join(dir, "failed")
		if _, err := os.Stat(failed); os.Getenv("FAKE_SMOLVM_FAIL_ONCE") == "" || errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(failed, nil, 0o600); err != nil {
				panic(err)
			}
			fmt.Fprintln(os.Stderr, cmp.Or(os.Getenv("FAKE_SMOLVM_FAIL_STDERR"), "fake failure"))
			return 1
		}
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
		if err := os.WriteFile(filepath.Join(dir, "idle-reclaim"), []byte(os.Getenv("SMOLVM_IDLE_RECLAIM")), 0o600); err != nil {
			panic(err)
		}
	case strings.HasPrefix(call, "machine delete "):
		machines = slices.DeleteFunc(machines, named)
	case strings.HasPrefix(call, "machine exec "):
		fmt.Print(os.Getenv("FAKE_SMOLVM_EXEC_STDOUT"))
	case strings.HasPrefix(call, "machine checkpoint "):
		if err := os.WriteFile(args[slices.Index(args, "-o")+1], []byte("checkpoint"), 0o600); err != nil {
			panic(err)
		}
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
	tmp := filepath.Join(dir, ".machines.json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		panic(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "machines.json")); err != nil {
		panic(err)
	}
}

// fake is a fake smolvm and an environment called alpha, whose state dir exists.
type fake struct {
	o   Options
	env state.Env
	dir string
}

// fakeSmolvm starts a fake smolvm whose machines are those that machines returns for alpha.
func fakeSmolvm(t *testing.T, machines func(state.Env) []smolvm.Machine) fake {
	t.Helper()
	source, err := Source(".")
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		o: Options{
			Name: "alpha", StateDir: t.TempDir(), Source: source, CacheDir: t.TempDir(),
			Command: "go run ./cmd/devenv", SmolVM: smolvm.CLI{Path: os.Args[0]},
		},
		dir: t.TempDir(),
	}
	if f.env, err = state.New(f.o.StateDir, f.o.Name); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMachines(f.dir, machines(f.env))
	t.Setenv("FAKE_SMOLVM_DIR", f.dir)
	t.Setenv("FAKE_SMOLVM_VERSION", "smolvm "+smolvm.Version)
	t.Setenv("SMOLVM_IDLE_RECLAIM", "")
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

// open holds alpha until the test ends.
func (f fake) open(t *testing.T) *Environment {
	t.Helper()
	e, err := open(f.env, f.o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func vm(s smolvm.State) func(state.Env) []smolvm.Machine {
	return func(env state.Env) []smolvm.Machine {
		if s == "" {
			return nil
		}
		return []smolvm.Machine{{Name: env.VM(), State: s}}
	}
}

// fakeHost stands in for /dev/kvm and fills f's download cache, so Up needs neither KVM nor the network.
func fakeHost(t *testing.T, f fake) {
	t.Helper()
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kvmDevice = device
	t.Cleanup(func() { kvmDevice = "/dev/kvm" })
	downloads, err := infra.Downloads(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(downloads, platform.Downloads()...) {
		path := filepath.Join(f.o.CacheDir, "downloads", "sha256", d.SHA256)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// lastRun is what a run leaves in the env dir.
var lastRun = []string{"mgmt.kubeconfig", "workload.kubeconfig", "ports.json", "ready", "logs/kind.log"}

func writeLastRun(t *testing.T, env state.Env) {
	t.Helper()
	for _, name := range lastRun {
		path := filepath.Join(env.Dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "guest.log"), []byte("kind create cluster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantGone(t *testing.T, env state.Env, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(env.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUpRefusesAReadyEnvironmentWhoseVMIsRunning(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	fakeHost(t, f)
	writeLastRun(t, f.env)

	_, err := Up(t.Context(), f.o)

	if want := "environment alpha is already up; use it, or delete it with: go run ./cmd/devenv down --name alpha"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"--version", "machine ls --json"}) {
		t.Errorf("smolvm calls = %q", got)
	}
	if !f.env.Ready() {
		t.Error("forgot that the running environment is ready")
	}
	if log, err := os.ReadFile(filepath.Join(f.env.Dir, "guest.log")); err != nil || string(log) != "kind create cluster\n" {
		t.Errorf("guest.log of the running environment = %q, %v", log, err)
	}
}

func TestUpMarksTheEnvironmentReadyOnceItIsUp(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	bringUp = func(*Environment, context.Context, smolvm.State) error { return nil }
	t.Cleanup(func() { bringUp = (*Environment).bringUp })

	e, err := Up(t.Context(), f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if !f.env.Ready() {
		t.Error("not ready")
	}
}

func TestUpReplacesARunningVMThatNeverBecameReady(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")

	if _, err := Up(t.Context(), f.o); err == nil || !strings.Contains(err.Error(), `stage "VM"`) {
		t.Errorf("err = %v", err)
	}

	if got := f.calls(t); !slices.Contains(got, "machine delete --name "+f.env.VM()+" -f") {
		t.Errorf("smolvm calls = %q; want the VM replaced", got)
	}
}

func TestUpExportsLogsAndDeletesTheVMWhenBringUpFails(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")

	_, err := Up(t.Context(), f.o)

	if err == nil || !strings.Contains(err.Error(), `stage "VM"`) || !strings.Contains(err.Error(), "logs: "+f.env.Dir) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Contains(got, "machine data-dir --name "+f.env.VM()) {
		t.Errorf("smolvm calls = %q; want the VM's console log exported", got)
	}
	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines after a failed up: %+v", got)
	}
	wantGone(t, f.env, "ports.json", "ready")
}

func TestUpReplacesALeftoverVMAndForgetsTheLastRun(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	fakeHost(t, f)
	writeLastRun(t, f.env)
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
}

func TestUpWithoutANameUsesTheOnlyEnvironment(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	fakeHost(t, f)
	writeLastRun(t, f.env)
	f.o.Name = ""

	if _, err := Up(t.Context(), f.o); err == nil || !strings.Contains(err.Error(), "environment alpha is already up") {
		t.Errorf("err = %v", err)
	}
}

func TestUpNamesTheOtherRunningEnvironments(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
	var progress bytes.Buffer
	f.o.Progress = &progress
	f.o.Name = "beta"

	if _, err := Up(t.Context(), f.o); err == nil {
		t.Fatal("no error")
	}

	if want := "] also running here, each in its own VM: alpha\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestUpChecksTheHostBeforeCreatingAnEnvironment(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_VERSION", "smolvm 1.21.0")
	f.o.Name = "beta"

	if _, err := Up(t.Context(), f.o); err == nil || !strings.Contains(err.Error(), "want smolvm "+smolvm.Version) {
		t.Errorf("err = %v", err)
	}

	if envs, err := state.List(f.o.StateDir); err != nil || len(envs) != 1 {
		t.Errorf("environments = %+v, %v; want only alpha", envs, err)
	}
}

func TestUpNeedsDevenvsSource(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	f.o.Source = ""

	if _, err := Up(t.Context(), f.o); !errors.Is(err, errNoSource) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q", got)
	}
}

func TestForgetDeletesWhatTheLastRunLeft(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	writeLastRun(t, f.env)
	e := f.open(t)

	if err := e.forget(); err != nil {
		t.Fatal(err)
	}

	wantGone(t, f.env, lastRun...)
	if log, err := os.ReadFile(filepath.Join(f.env.Dir, "guest.log")); err != nil || len(log) > 0 {
		t.Errorf("guest.log = %q, %v", log, err)
	}
}

func TestFailAfterAnInterruptDeletesTheVMWithoutExportingLogs(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	e := f.open(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := e.fail(ctx, fmt.Errorf("stage %q: %w", "VM", context.Canceled))

	if err == nil || err.Error() != "interrupted; deleted the VM" {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"machine ls --json", "machine delete --name " + f.env.VM() + " -f"}) {
		t.Errorf("smolvm calls = %q", got)
	}
}

func TestFailRetainsTheVMWhenAsked(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	f.o.Retain = true
	e := f.open(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := e.fail(ctx, context.Canceled)

	if want := "kept the VM; delete it with: go run ./cmd/devenv down --name alpha"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v", err)
	}
	if got := f.machines(); len(got) != 1 {
		t.Errorf("machines = %+v", got)
	}
}

func TestDeleteFinishesAfterAnInterrupt(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	writeLastRun(t, f.env)
	e := f.open(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if existed, err := e.Delete(ctx, false); !existed || err != nil {
		t.Errorf("Delete() = %v, %v", existed, err)
	}

	if got := f.machines(); len(got) > 0 {
		t.Errorf("machines = %+v", got)
	}
	wantGone(t, f.env, "mgmt.kubeconfig", "workload.kubeconfig", "ports.json", "ready")
}

func TestCreateVMWaitsWhileAnotherEnvironmentStartsItsVM(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	e := f.open(t)
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

func TestBuildWaitsWhileAnotherEnvironmentBuilds(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	e := f.open(t)
	unlock, err := state.WaitLock(t.Context(), filepath.Join(f.o.CacheDir, "build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	if _, err := e.build(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestCreateVMReplacesALeftoverVMAndPublishesTheRecordedPorts(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	env := f.env
	e := f.open(t)

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
	if reclaim, err := os.ReadFile(filepath.Join(f.dir, "idle-reclaim")); string(reclaim) != "off" {
		t.Errorf("SMOLVM_IDLE_RECLAIM = %q, %v; want off", reclaim, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := state.WaitLock(ctx, e.startLock()); err != nil {
		t.Errorf("createVM kept the start lock: %v", err)
	} else {
		unlock()
	}
}

func TestOpenRequiresARunningVM(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	writeLastRun(t, f.env)

	_, err := Open(t.Context(), f.o)

	if err == nil || !strings.Contains(err.Error(), "environment alpha is not running") {
		t.Errorf("err = %v", err)
	}
	if _, err := f.env.Lock(); err != nil {
		t.Errorf("Open did not release the environment: %v", err)
	}
}

func TestOpenRequiresAReadyEnvironment(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))

	_, err := Open(t.Context(), f.o)

	if want := "environment alpha never became ready; bring it up again with: go run ./cmd/devenv up --name alpha"; err == nil || err.Error() != want {
		t.Errorf("err = %v", err)
	}
}

func TestOpenReadsTheRecordedPorts(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	writeLastRun(t, f.env)
	want := state.Ports{MgmtAPI: 1, WorkloadAPI: 2, Registry: 3}
	if err := f.env.WritePorts(want); err != nil {
		t.Fatal(err)
	}

	e, err := Open(t.Context(), f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if e.Ports != want {
		t.Errorf("Ports = %+v, want %+v", e.Ports, want)
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
