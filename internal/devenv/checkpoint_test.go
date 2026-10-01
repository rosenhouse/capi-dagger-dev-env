package devenv

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

func TestPlatformInputsAreWhatTheCodeUses(t *testing.T) {
	in, err := describePlatform("exact-v1-ab")
	if err != nil {
		t.Fatal(err)
	}

	wantDownloads, err := downloads()
	if err != nil {
		t.Fatal(err)
	}
	platformScripts, err := platform.Scripts(WorkloadCluster, WorkloadNamespace)
	if err != nil {
		t.Fatal(err)
	}
	want := platformInputs{
		SmolVM:    smolvm.Version,
		Contract:  "exact-v1-ab",
		Machine:   infra.MachineConfig(state.Ports{}),
		Downloads: wantDownloads,
		Images:    infra.Images(),
		Scripts:   append(infra.Scripts(WorkloadCluster, WorkloadNamespace), platformScripts...),
	}
	if got, want := marshal(t, in), marshal(t, want); got != want {
		t.Errorf("inputs\n%s\nwant\n%s", got, want)
	}
}

func TestPlatformKeyChangesWithEveryInput(t *testing.T) {
	in, err := describePlatform("exact-v1-ab")
	if err != nil {
		t.Fatal(err)
	}
	base := in.key()
	edits := map[string]func(*platformInputs){
		"smolvm":      func(in *platformInputs) { in.SmolVM += "-rc" },
		"contract":    func(in *platformInputs) { in.Contract = smolvm.UnknownContract },
		"CPUs":        func(in *platformInputs) { in.Machine.CPUs++ },
		"memory":      func(in *platformInputs) { in.Machine.MemoryMiB++ },
		"storage":     func(in *platformInputs) { in.Machine.StorageGiB++ },
		"overlay":     func(in *platformInputs) { in.Machine.OverlayGiB++ },
		"guest ports": func(in *platformInputs) { in.Machine.Ports = in.Machine.Ports[1:] },
	}
	for i := range in.Downloads {
		edits[fmt.Sprintf("download %d sha256", i)] = func(in *platformInputs) { in.Downloads[i].SHA256 = "0" }
		edits[fmt.Sprintf("download %d path", i)] = func(in *platformInputs) { in.Downloads[i].Path += "x" }
	}
	for i := range in.Images {
		edits[fmt.Sprintf("image %d", i)] = func(in *platformInputs) { in.Images[i] += "x" }
	}
	for i := range in.Scripts {
		edits[fmt.Sprintf("script %d", i)] = func(in *platformInputs) { in.Scripts[i] += "\n" }
	}
	for name, edit := range edits {
		edited, err := describePlatform("exact-v1-ab")
		if err != nil {
			t.Fatal(err)
		}
		edit(&edited)
		if edited.key() == base {
			t.Errorf("changing the %s left the key %s", name, base)
		}
	}
	if len(in.Downloads) == 0 || len(in.Images) == 0 || len(in.Scripts) == 0 || len(in.Machine.Ports) != 3 {
		t.Errorf("inputs lack downloads, images, scripts or ports: %s", marshal(t, in))
	}
}

func TestPlatformKeyIsStable(t *testing.T) {
	a, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}
	b, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}
	if a.key() != b.key() || len(a.key()) != 32 {
		t.Errorf("keys %s and %s; want the same 32 hex digits", a.key(), b.key())
	}
}

// captured are the host ports that the fake checkpoint publishes.
var captured = []smolvm.Port{{Host: 16443, Guest: 6443}, {Host: 17443, Guest: 7443}, {Host: 15000, Guest: 5000}}

// fakeCheckpoint puts a checkpoint for this host's platform in f's cache, and returns its path.
func fakeCheckpoint(t *testing.T, f fake) string {
	t.Helper()
	in, err := hostPlatform()
	if err != nil {
		t.Fatal(err)
	}
	path := f.o.platformCache().checkpoint(in.key())
	writeCheckpoint(t, path, captured...)
	return path
}

func TestStartRestoredPublishesFreshPortsInPlaceOfTheCapturedOnes(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Created))
	e := f.open(t)

	if err := e.startRestored(t.Context(), fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	p, err := f.env.ReadPorts()
	if err != nil || p != e.Ports {
		t.Fatalf("recorded ports %+v, %v; want %+v", p, err, e.Ports)
	}
	rebind := fmt.Sprintf("machine update --name %s --remove-port 16443:6443 --remove-port 17443:7443 --remove-port 15000:5000 -p %d:6443 -p %d:7443 -p %d:5000",
		f.env.VM(), p.MgmtAPI, p.WorkloadAPI, p.Registry)
	if got := f.calls(t); len(got) != 2 || got[0] != rebind || !strings.HasPrefix(got[1], "machine start --name "+f.env.VM()) {
		t.Errorf("smolvm calls = %q; want %q, then a start", got, rebind)
	}
	if reclaim, err := os.ReadFile(filepath.Join(f.dir, "idle-reclaim")); string(reclaim) != "off" {
		t.Errorf("SMOLVM_IDLE_RECLAIM = %q, %v; want off", reclaim, err)
	}
	wantStartLockFree(t, e)
}

const agentTimeout = "Error: agent operation failed: wait for ready: clone agent did not respond to ping within timeout (socket_exists=false)"

func TestStartRestoredStartsAgainOnceWhenTheVMDoesNotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, once string
		starts             int
		ok                 bool
	}{
		{"once", agentTimeout, "1", 2, true},
		{"twice", agentTimeout, "", 2, false},
		{"another failure", "Error: host port 16443 is already in use", "", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSmolvm(t, vm(smolvm.Created))
			t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
			t.Setenv("FAKE_SMOLVM_FAIL_STDERR", tc.stderr)
			t.Setenv("FAKE_SMOLVM_FAIL_ONCE", tc.once)
			e := f.open(t)

			err := e.startRestored(t.Context(), fakeCheckpoint(t, f))

			if (err == nil) != tc.ok {
				t.Errorf("err = %v", err)
			}
			starts := 0
			for _, call := range f.calls(t) {
				if strings.HasPrefix(call, "machine start ") {
					starts++
				}
			}
			if starts != tc.starts {
				t.Errorf("%d starts; want %d", starts, tc.starts)
			}
		})
	}
}

func TestUpRestoresThePlatformCheckpoint(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	checkpoint := fakeCheckpoint(t, f)
	var progress bytes.Buffer
	f.o.Progress = &progress

	_, _ = Up(t.Context(), f.o)

	got := f.calls(t)
	restore := slices.Index(got, "machine create --name "+f.env.VM()+" --from "+checkpoint)
	if restore < 0 || !strings.HasPrefix(got[restore+1], "machine update --name "+f.env.VM()) || !strings.HasPrefix(got[restore+2], "machine start --name "+f.env.VM()) {
		t.Errorf("smolvm calls = %q; want a restore, a port rebind and a start", got)
	}
	if want := "] platform: restoring " + checkpoint + "\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestUpStartsColdWhenTheRestoreFails(t *testing.T) {
	for _, fail := range []string{"machine create --name %s --from", "machine start --name %s"} {
		t.Run(fail, func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			fakeCheckpoint(t, f)
			t.Setenv("FAKE_SMOLVM_FAIL", fmt.Sprintf(fail, f.env.VM()))
			t.Setenv("FAKE_SMOLVM_FAIL_ONCE", "1")
			var progress bytes.Buffer
			f.o.Progress = &progress

			_, _ = Up(t.Context(), f.o)

			got := f.calls(t)
			failed := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, fmt.Sprintf(fail, f.env.VM())) })
			cold := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --net") })
			if failed < 0 || cold < failed {
				t.Errorf("smolvm calls = %q; want a cold create after the failed restore", got)
			}
			if fail == "machine start --name %s" && !slices.Contains(got[failed:cold], "machine delete --name "+f.env.VM()+" -f") {
				t.Errorf("smolvm calls = %q; want the restored VM deleted before the cold create", got)
			}
			if want := "] platform: cold start, because restoring it failed: "; !strings.Contains(progress.String(), want) {
				t.Errorf("progress = %q; want %q", progress.String(), want)
			}
			if fail == "machine start --name %s" && !slices.Contains(got[failed:cold], "machine data-dir --name "+f.env.VM()) {
				t.Errorf("smolvm calls = %q; want the restored VM's console log kept", got)
			}
		})
	}
}

func TestUpStartsColdWithoutACheckpointOrWhenAsked(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(fmt.Sprint(cold), func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			want := "] platform: cold start, because --cold asked for one\n"
			if cold {
				fakeCheckpoint(t, f)
			} else {
				in, err := hostPlatform()
				if err != nil {
					t.Fatal(err)
				}
				want = "] platform: cold start, because no platform checkpoint " + f.o.platformCache().checkpoint(in.key()) + "\n"
			}
			t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
			var progress bytes.Buffer
			f.o.Progress, f.o.Cold = &progress, cold

			_, _ = Up(t.Context(), f.o)

			if !strings.Contains(progress.String(), want) {
				t.Errorf("progress = %q; want %q", progress.String(), want)
			}
			if got := f.calls(t); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, " --from ") }) {
				t.Errorf("smolvm calls = %q; want no restore", got)
			}
		})
	}
}

func TestPlatformLetsTheBuildStartBeforeARestoreAndAfterAColdBoot(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(fmt.Sprint(cold), func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			fakeCheckpoint(t, f)
			t.Setenv("FAKE_SMOLVM_FAIL", "machine exec")
			f.o.Cold = cold
			e := f.open(t)
			var before []string
			called := false

			_ = e.platform(t.Context(), "", func() {
				if !called {
					called, before = true, f.calls(t)
				}
			})

			restored := slices.ContainsFunc(before, func(c string) bool { return strings.Contains(c, " --from ") })
			booted := slices.ContainsFunc(before, func(c string) bool { return strings.HasPrefix(c, "machine start ") })
			if !called || restored || booted != cold {
				t.Errorf("smolvm calls before the build = %q; want none for a restore, and a start for a cold boot", before)
			}
		})
	}
}

func TestRestoreWaitsWhileAnotherEnvironmentRestores(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	e := f.open(t)
	unlock, err := state.WaitLock(t.Context(), filepath.Join(f.o.CacheDir, "restore.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	if err := e.warmPlatform(ctx, "", fakeCheckpoint(t, f)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q", got)
	}
}

func TestPlatformDoesNotStartColdAfterAnInterruptedRestore(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	fakeCheckpoint(t, f)
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := e.platform(ctx, "", func() {}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(progress.String(), "cold start") {
		t.Errorf("progress = %q; want no cold start", progress.String())
	}
}

func TestPlatformCacheSaveMakesAnEntryOnceTheCaptureSucceeds(t *testing.T) {
	c := platformCache{dir: filepath.Join(t.TempDir(), "platform")}
	in, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}
	var tmp string

	path, err := c.save(in, func(file string) error {
		tmp = file
		if _, err := c.lookup(in.key()); err == nil {
			t.Error("the entry exists during the capture")
		}
		return os.WriteFile(file, []byte("checkpoint"), 0o600)
	})

	if err != nil || path != c.checkpoint(in.key()) {
		t.Fatalf("save() = %q, %v", path, err)
	}
	if got, err := os.ReadFile(path); string(got) != "checkpoint" {
		t.Errorf("checkpoint = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(c.dir, in.key()+".json")); err != nil || string(got) != marshal(t, in) {
		t.Errorf("inputs = %s, %v", got, err)
	}
	if !strings.HasSuffix(tmp, ".checkpoint") || filepath.Dir(tmp) != c.dir {
		t.Errorf("captured to %s; want a .checkpoint file in %s, for smolvm and an atomic rename", tmp, c.dir)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v", tmp, err)
	}
}

func TestPlatformCacheSaveLeavesNothingWhenTheCaptureFails(t *testing.T) {
	c := platformCache{dir: filepath.Join(t.TempDir(), "platform")}
	in, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.save(in, func(file string) error {
		if err := os.WriteFile(file, []byte("part"), 0o600); err != nil {
			t.Fatal(err)
		}
		return errors.New("capture failed")
	})

	if err == nil || err.Error() != "capture failed" {
		t.Errorf("err = %v", err)
	}
	if entries, err := os.ReadDir(c.dir); err != nil || len(entries) > 0 {
		t.Errorf("cache holds %v, %v", entries, err)
	}
}

func TestSavePlatformRefusesToReplaceACheckpoint(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	path := fakeCheckpoint(t, f)

	_, err := SavePlatform(t.Context(), f.o, false)

	if want := path + " exists; replace it with: go run ./cmd/devenv platform save --force"; err == nil || err.Error() != want {
		t.Errorf("err = %v; want %s", err, want)
	}
	if got := f.calls(t); !slices.Equal(got, []string{"--version"}) {
		t.Errorf("smolvm calls = %q", got)
	}
}

func TestSavePlatformDeletesTheVMWhenBringUpFails(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine exec")

	if _, err := SavePlatform(t.Context(), f.o, false); err == nil || !strings.Contains(err.Error(), `stage "guest tools"`) {
		t.Errorf("err = %v", err)
	}

	got := f.calls(t)
	if !slices.ContainsFunc(got, func(c string) bool {
		return strings.HasPrefix(c, "machine start ") && strings.Contains(c, " --branchable")
	}) {
		t.Errorf("smolvm calls = %q; want a branchable start", got)
	}
	if machines := f.machines(); len(machines) > 0 {
		t.Errorf("machines = %+v", machines)
	}
	if entries, err := os.ReadDir(f.o.platformCache().dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("platform cache holds %v, %v", entries, err)
	}
}

func TestCaptureZeroFillsTheGuestAndDeletesTheVM(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Running))
	t.Setenv("FAKE_SMOLVM_EXEC_STDOUT", "2600\n")
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)
	file := filepath.Join(t.TempDir(), "x.checkpoint")

	if err := e.capture(t.Context(), file); err != nil {
		t.Fatal(err)
	}

	got := f.calls(t)
	checkpoint := slices.Index(got, "machine checkpoint --name "+f.env.VM()+" -o "+file)
	if checkpoint < 0 || got[len(got)-1] != "machine delete --name "+f.env.VM()+" -f" {
		t.Errorf("smolvm calls = %q; want a checkpoint, then a delete", got)
	}
	if !slices.ContainsFunc(got[:checkpoint], func(c string) bool { return strings.Contains(c, "mount -t tmpfs") }) {
		t.Errorf("smolvm calls = %q; want a zero fill before the checkpoint", got)
	}
	if want := "] zero-filled "; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestMemAvailableMiB(t *testing.T) {
	for meminfo, want := range map[string]int{
		"MemTotal:       16374652 kB\nMemFree:         1041804 kB\nMemAvailable:   12582912 kB\n": 12288,
		"MemTotal:       16374652 kB\n": 0,
		"MemAvailable:   lots kB\n":     0,
	} {
		if got := memAvailableMiB(meminfo); got != want {
			t.Errorf("memAvailableMiB(%q) = %d; want %d", meminfo, got, want)
		}
	}
}

func wantStartLockFree(t *testing.T, e *Environment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	unlock, err := state.WaitLock(ctx, e.startLock())
	if err != nil {
		t.Errorf("the start lock is held: %v", err)
		return
	}
	unlock()
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeCheckpoint writes a file that smolvm.CheckpointPorts reads as a checkpoint publishing ports.
func writeCheckpoint(t *testing.T, path string, ports ...smolvm.Port) {
	t.Helper()
	var network struct {
		Ports []struct {
			Host  int `json:"host"`
			Guest int `json:"guest"`
		} `json:"ports"`
	}
	for _, p := range ports {
		network.Ports = append(network.Ports, struct {
			Host  int `json:"host"`
			Guest int `json:"guest"`
		}{p.Host, p.Guest})
	}
	manifest, err := json.Marshal(map[string]any{"checkpoint": map[string]any{"network": network}})
	if err != nil {
		t.Fatal(err)
	}
	footer := make([]byte, 64)
	copy(footer, "SMOLPACK")
	binary.LittleEndian.PutUint64(footer[44:], uint64(len(manifest)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, slices.Concat(manifest, footer), 0o644); err != nil {
		t.Fatal(err)
	}
}
