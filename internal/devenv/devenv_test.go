package devenv

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// TestMain lets the test binary stand in for smolvm: with FAKE_SMOLVM_CALLS set, it appends its arguments
// there, prints FAKE_SMOLVM_LS for "machine ls --json", and prints smolvm's version for "--version".
func TestMain(m *testing.M) {
	if calls := os.Getenv("FAKE_SMOLVM_CALLS"); calls != "" {
		f, err := os.OpenFile(calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
		f.Close()
		switch strings.Join(os.Args[1:], " ") {
		case "--version":
			fmt.Println("smolvm " + smolvm.Version)
		case "machine ls --json":
			fmt.Println(os.Getenv("FAKE_SMOLVM_LS"))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeSmolvm returns options for an environment called alpha whose smolvm lists machines,
// and a function that returns smolvm's calls so far.
func fakeSmolvm(t *testing.T, machines func(state.Env) []smolvm.Machine) (Options, state.Env, func() []string) {
	t.Helper()
	o := Options{Name: "alpha", StateDir: t.TempDir(), SmolVM: smolvm.CLI{Path: os.Args[0]}}
	env, err := state.New(o.StateDir, o.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_SMOLVM_CALLS", calls)
	t.Setenv("FAKE_SMOLVM_LS", lsJSON(machines(env)))
	return o, env, func() []string {
		data, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func lsJSON(machines []smolvm.Machine) string {
	var items []string
	for _, m := range machines {
		items = append(items, fmt.Sprintf(`{"name": %q, "state": %q, "parent_machine": null}`, m.Name, m.State))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func vm(s smolvm.State) func(state.Env) []smolvm.Machine {
	return func(env state.Env) []smolvm.Machine {
		if s == "" {
			return nil
		}
		return []smolvm.Machine{{Name: env.VM(), State: s}}
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
	device := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kvmDevice = device
	t.Cleanup(func() { kvmDevice = "/dev/kvm" })
	o, env, calls := fakeSmolvm(t, vm(smolvm.Running))
	writeKubeconfigs(t, env)
	if err := os.WriteFile(filepath.Join(env.Dir, "guest.log"), []byte("kind create cluster\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Up(t.Context(), o)

	if err == nil || !strings.Contains(err.Error(), "environment alpha is already up") {
		t.Errorf("err = %v", err)
	}
	if got := calls(); !slices.Equal(got, []string{"--version", "machine ls --json"}) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "mgmt.kubeconfig")); err != nil {
		t.Errorf("kubeconfig of the running environment: %v", err)
	}
	if log, err := os.ReadFile(filepath.Join(env.Dir, "guest.log")); err != nil || string(log) != "kind create cluster\n" {
		t.Errorf("guest.log of the running environment = %q, %v", log, err)
	}
}

func TestDownDeletesTheVMAndKubeconfigs(t *testing.T) {
	o, env, calls := fakeSmolvm(t, vm(smolvm.Running))
	writeKubeconfigs(t, env)
	var out bytes.Buffer

	if err := Down(t.Context(), o, false, &out); err != nil {
		t.Fatal(err)
	}

	if got := calls(); !slices.Contains(got, "machine delete --name "+env.VM()+" -f") {
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
	o, env, calls := fakeSmolvm(t, vm(""))
	var out bytes.Buffer

	if err := Down(t.Context(), o, true, &out); err != nil {
		t.Fatal(err)
	}

	if got := calls(); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, "delete") }) {
		t.Errorf("smolvm calls = %q", got)
	}
	if _, err := os.Stat(env.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state dir: %v", err)
	}
	if want := "Environment alpha has no VM.\nDeleted " + env.Dir + ".\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestDownWaitsForNoOtherCommand(t *testing.T) {
	o, env, _ := fakeSmolvm(t, vm(smolvm.Running))
	unlock, err := env.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = Down(t.Context(), o, true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "another devenv command is using environment alpha") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(env.Dir); err != nil {
		t.Errorf("state dir: %v", err)
	}
}

func TestOpenRequiresARunningVM(t *testing.T) {
	o, env, _ := fakeSmolvm(t, vm(smolvm.Stopped))
	writeKubeconfigs(t, env)

	_, err := Open(t.Context(), o)

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
		if err := e.Redeploy(t.Context(), version); err == nil || !strings.Contains(err.Error(), "version") {
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
