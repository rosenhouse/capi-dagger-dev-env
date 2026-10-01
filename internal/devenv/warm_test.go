package devenv

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// captured are the host ports that the fake checkpoint publishes.
var captured = []smolvm.Port{{Host: 16443, Guest: 6443}, {Host: 17443, Guest: 7443}, {Host: 15000, Guest: 5000}}

// fakeCheckpoint saves a checkpoint for this host's platform in f's cache, and returns its path.
func fakeCheckpoint(t *testing.T, f fake) string {
	t.Helper()
	in, err := hostPlatform()
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.o.platformCache().save(in, func(file string) error {
		writeCheckpoint(t, file, captured...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// age makes this host's platform checkpoint in f's cache look captured age ago.
func age(t *testing.T, f fake, age time.Duration) {
	t.Helper()
	in, err := hostPlatform()
	if err != nil {
		t.Fatal(err)
	}
	c := f.o.platformCache()
	data, err := json.Marshal(platformEntry{CapturedAt: time.Now().Add(-age), Inputs: in})
	if err == nil {
		err = os.WriteFile(c.entry(in.key()), data, 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// guestKubeconfig is a kubeconfig as the guest prints it.
const guestKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: kind-mgmt
  cluster: {server: "https://0.0.0.0:6443"}
users:
- name: kind-mgmt
  user: {token: t}
contexts:
- name: kind-mgmt
  context: {cluster: kind-mgmt, user: kind-mgmt}
current-context: kind-mgmt
`

// stubRestoredGates stands in for the gates of a restored platform, and has the guest print kubeconfigs.
func stubRestoredGates(t *testing.T, gates func(e *Environment, ctx context.Context, restored time.Time) error) {
	t.Helper()
	restoredGates = gates
	t.Cleanup(func() { restoredGates = (*Environment).restoredGates })
	t.Setenv("FAKE_SMOLVM_EXEC_STDOUT", guestKubeconfig)
}

func passGates(*Environment, context.Context, time.Time) error { return nil }

func TestStartRestoredPublishesFreshPortsInPlaceOfTheCapturedOnes(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Created))
	e := f.open(t)

	if _, err := e.startRestored(t.Context(), captured); err != nil {
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
			var progress bytes.Buffer
			f.o.Progress = &progress
			e := f.open(t)

			_, err := e.startRestored(t.Context(), captured)

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
			if said := strings.Contains(progress.String(), "] the restored VM did not answer in time"); said != (tc.starts == 2) {
				t.Errorf("progress = %q", progress.String())
			}
		})
	}
}

func TestPlatformRestoresTheCheckpoint(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	stubRestoredGates(t, passGates)
	checkpoint := fakeCheckpoint(t, f)
	age(t, f, 2*time.Hour)
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)

	if err := e.platform(t.Context(), "", func() {}); err != nil {
		t.Fatal(err)
	}

	got := f.calls(t)
	restore := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --from ") })
	if restore < 0 || !strings.HasPrefix(got[restore+1], "machine update --name "+f.env.VM()) || !strings.HasPrefix(got[restore+2], "machine start --name "+f.env.VM()) ||
		slices.ContainsFunc(got[restore+3:], func(c string) bool { return !strings.HasPrefix(c, "machine exec ") }) {
		t.Errorf("smolvm calls = %q; want a restore, a port rebind and a start, then only guest commands", got)
	}
	if want := "] platform: restoring " + checkpoint + ", captured 2h0m0s ago\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestWarmPlatformDeletesALeftoverVMBeforeRestoring(t *testing.T) {
	f := fakeSmolvm(t, vm(smolvm.Stopped))
	stubRestoredGates(t, passGates)
	e := f.open(t)

	if err := e.warmPlatform(t.Context(), smolvm.Stopped, fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	got := f.calls(t)
	deleted := slices.Index(got, "machine delete --name "+f.env.VM()+" -f")
	restored := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --from ") })
	if deleted < 0 || restored < deleted {
		t.Errorf("smolvm calls = %q; want the leftover deleted before the restore", got)
	}
}

func TestWarmPlatformPointsTheKubeconfigsAtTheFreshPorts(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	stubRestoredGates(t, passGates)
	e := f.open(t)

	if err := e.warmPlatform(t.Context(), "", fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	for path, port := range map[string]int{e.MgmtKubeconfig: e.Ports.MgmtAPI, e.WorkloadKubeconfig: e.Ports.WorkloadAPI} {
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if want := "https://localhost:" + strconv.Itoa(port); err != nil || cfg.Host != want {
			t.Errorf("%s: server %v, %v; want %s", path, cfg, err, want)
		}
	}
}

// KCP rotates the workload cluster's kubeconfig in its Secret.
func TestWarmPlatformReadsTheWorkloadKubeconfigFromItsSecret(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	stubRestoredGates(t, passGates)
	e := f.open(t)

	if err := e.warmPlatform(t.Context(), "", fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	read := "-c kubectl -n default get secret work-kubeconfig -o jsonpath='{.data.value}' | base64 -d"
	if got := f.calls(t); !slices.ContainsFunc(got, func(c string) bool { return strings.HasSuffix(c, read) }) {
		t.Errorf("smolvm calls = %q; want one ending %q", got, read)
	}
}

func TestWarmPlatformGatesOnWhatHappenedSinceBeforeTheStart(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	t.Setenv("FAKE_SMOLVM_SLEEP", "machine start=300ms")
	var sinceRestored time.Duration
	stubRestoredGates(t, func(_ *Environment, _ context.Context, restored time.Time) error {
		sinceRestored = time.Since(restored)
		return nil
	})
	e := f.open(t)

	if err := e.warmPlatform(t.Context(), "", fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	if sinceRestored < 300*time.Millisecond {
		t.Errorf("the gates began %v after the restore time; want it from before the 300ms start", sinceRestored)
	}
}

func TestPlatformStartsColdWhenTheRestoredPlatformFailsItsGates(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	stubRestoredGates(t, func(*Environment, context.Context, time.Time) error { return errors.New("leases not renewed") })
	fakeCheckpoint(t, f)
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)

	_ = e.platform(t.Context(), "", func() {})

	if got := f.calls(t); !slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --net") }) {
		t.Errorf("smolvm calls = %q; want a cold create", got)
	}
	if want := `] platform: cold start, because restoring it failed: stage "platform gates": leases not renewed`; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestWarmPlatformReadsTheCapturedPortsBeforeRestoring(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	e := f.open(t)
	checkpoint := fakeCheckpoint(t, f)
	if err := os.WriteFile(checkpoint, []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := e.warmPlatform(t.Context(), "", checkpoint)

	if err == nil || !strings.Contains(err.Error(), "too short for a checkpoint") {
		t.Errorf("err = %v", err)
	}
	if got := f.calls(t); len(got) > 0 {
		t.Errorf("smolvm calls = %q; want none", got)
	}
}

func TestWarmPlatformRestoresALinkThatASaveCannotReplace(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	stubRestoredGates(t, passGates)
	e := f.open(t)
	checkpoint := fakeCheckpoint(t, f)

	if err := e.warmPlatform(t.Context(), "", checkpoint); err != nil {
		t.Fatal(err)
	}

	got := f.calls(t)
	restore := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --from ") })
	if restore < 0 {
		t.Fatalf("smolvm calls = %q; want a restore", got)
	}
	from := strings.TrimPrefix(got[restore], "machine create --name "+f.env.VM()+" --from ")
	restored, err := os.Stat(filepath.Join(f.dir, "restored-from"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := os.Stat(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if from == checkpoint || !os.SameFile(restored, entry) {
		t.Errorf("restored %s; want a link to %s", from, checkpoint)
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v; want the link removed", from, err)
	}
}

func TestTwoEnvironmentsRestoreOneCheckpointAtOnce(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	// The first restore's gates outlast the second's restore.
	stubRestoredGates(t, func(*Environment, context.Context, time.Time) error { time.Sleep(200 * time.Millisecond); return nil })
	t.Setenv("FAKE_SMOLVM_SLEEP", "machine create=300ms")
	checkpoint := fakeCheckpoint(t, f)
	beta, err := state.New(f.o.StateDir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	var g errgroup.Group
	for _, env := range []state.Env{f.env, beta} {
		e, err := open(env, f.o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		g.Go(func() error { return e.warmPlatform(t.Context(), "", checkpoint) })
	}

	if err := g.Wait(); err != nil {
		t.Error(err)
	}
}

func TestWaitingForAnotherEnvironmentsRestoreIsAStageOfItsOwn(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	stubRestoredGates(t, passGates)
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)
	unlock, err := state.WaitLock(t.Context(), filepath.Join(f.o.CacheDir, "restore.lock"))
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, unlock)

	if err := e.warmPlatform(t.Context(), "", fakeCheckpoint(t, f)); err != nil {
		t.Fatal(err)
	}

	if wait, began := progressSeconds(t, progress.String(), "wait for another environment's restore: ([0-9.]+)s"), progressSeconds(t, progress.String(), `\[ *([0-9.]+)s\] restore VM`); wait < 0.3 || began < wait {
		t.Errorf("progress = %q; want a 0.3 s wait, then the restore", progress.String())
	}
}

// progressSeconds returns the seconds that pattern's group matches on a line of progress.
func progressSeconds(t *testing.T, progress, pattern string) float64 {
	t.Helper()
	m := regexp.MustCompile(`(?m)` + pattern + `$`).FindStringSubmatch(progress)
	if m == nil {
		t.Fatalf("progress = %q; want a line matching %s", progress, pattern)
	}
	s, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInterruptedRestoreWaitsForSmolvmToFinish(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	t.Setenv("FAKE_SMOLVM_SLEEP", "machine create=500ms")
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	err := e.warmPlatform(ctx, "", fakeCheckpoint(t, f))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if want := "] interrupted; devenv deletes the VM once smolvm has restored it\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
	if got := f.calls(t); len(got) != 1 {
		t.Errorf("smolvm calls = %q; want only the restore", got)
	}
}

func TestRestoredGatesWaitForLeasesWithinABound(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	e := f.open(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	for _, cluster := range []string{"mgmt", "workload"} {
		if _, err := e.WriteKubeconfig(cluster, []byte(guestKubeconfig), closed); err != nil {
			t.Fatal(err)
		}
	}
	warmGatesTimeout = 300 * time.Millisecond
	t.Cleanup(func() { warmGatesTimeout = 2 * time.Minute })
	start := time.Now()

	err = e.restoredGates(t.Context(), time.Now())

	if want := `gate "management leases renewed since the restore": a restored platform passes its gates within 300ms; last check: `; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("err = %v; want it to start with %s", err, want)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
}

func TestPlatformFailsInsteadOfStartingColdWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name             string
		checkpoint, warm bool
		retain           bool
		want             string
	}{
		{name: "--warm without a checkpoint", warm: true, want: "no platform checkpoint "},
		{name: "--warm", checkpoint: true, warm: true, want: `stage "start VM"`},
		{name: "--retain", checkpoint: true, retain: true, want: `stage "start VM"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			if tc.checkpoint {
				fakeCheckpoint(t, f)
			}
			t.Setenv("FAKE_SMOLVM_FAIL", "machine start --name "+f.env.VM())
			t.Setenv("FAKE_SMOLVM_FAIL_ONCE", "1")
			f.o.Warm, f.o.Retain = tc.warm, tc.retain
			e := f.open(t)

			err := e.platform(t.Context(), "", func() {})

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v; want it to contain %q", err, tc.want)
			}
			got := f.calls(t)
			if slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, " --net") || strings.HasPrefix(c, "machine delete ") }) {
				t.Errorf("smolvm calls = %q; want neither a cold create nor a delete", got)
			}
		})
	}
}

func TestPlatformStartsColdWhenTheRestoreFails(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		vmLeft     bool
	}{
		{name: "create", fail: "machine create --name %s --from"},
		{name: "start", fail: "machine start --name %s", vmLeft: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			fakeCheckpoint(t, f)
			fail := fmt.Sprintf(tc.fail, f.env.VM())
			t.Setenv("FAKE_SMOLVM_FAIL", fail)
			t.Setenv("FAKE_SMOLVM_FAIL_ONCE", "1")
			var progress bytes.Buffer
			f.o.Progress = &progress
			e := f.open(t)

			_ = e.platform(t.Context(), "", func() {})

			got := f.calls(t)
			failed := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, fail) })
			cold := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --net") })
			if failed < 0 || cold < failed {
				t.Fatalf("smolvm calls = %q; want a cold create after the failed restore", got)
			}
			if tc.vmLeft && !slices.Contains(got[failed:cold], "machine delete --name "+f.env.VM()+" -f") {
				t.Errorf("smolvm calls = %q; want the restored VM deleted before the cold create", got)
			}
			if tc.vmLeft && !slices.Contains(got[failed:cold], "machine data-dir --name "+f.env.VM()) {
				t.Errorf("smolvm calls = %q; want the restored VM's console log kept", got)
			}
			if want := "] platform: cold start, because restoring it failed: "; !strings.Contains(progress.String(), want) {
				t.Errorf("progress = %q; want %q", progress.String(), want)
			}
		})
	}
}

func TestPlatformHintsHowToReplaceACheckpointThatFailedToRestore(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	fakeCheckpoint(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine create --name "+f.env.VM()+" --from")
	t.Setenv("FAKE_SMOLVM_FAIL_ONCE", "1")
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)

	_ = e.platform(t.Context(), "", func() {})

	if want := "\nIf restores keep failing, replace the checkpoint with: go run ./cmd/devenv platform save --force\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
}

func TestPlatformExportsTheLogsOfARestoredVMThatFails(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	stubRestoredGates(t, func(*Environment, context.Context, time.Time) error { return errors.New("leases not renewed") })
	fakeCheckpoint(t, f)
	e := f.open(t)

	_ = e.platform(t.Context(), "", func() {})

	got := f.calls(t)
	export := slices.Index(got, "machine cp "+f.env.VM()+":/tmp/devenv-logs.tgz "+filepath.Join(f.env.Dir, "logs", "restore", ".guest-logs.tgz"))
	cold := slices.IndexFunc(got, func(c string) bool { return strings.HasPrefix(c, "machine create --name "+f.env.VM()+" --net") })
	if export < 0 || cold < export {
		t.Errorf("smolvm calls = %q; want the guest's logs exported before the cold start", got)
	}
}

func TestPlatformStartsColdFromACheckpointOlderThan35Days(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	checkpoint := fakeCheckpoint(t, f)
	age(t, f, 36*24*time.Hour)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
	var progress bytes.Buffer
	f.o.Progress = &progress
	e := f.open(t)

	_ = e.platform(t.Context(), "", func() {})

	if want := "] platform: cold start, because " + checkpoint + " was captured 36 days ago; replace it with: go run ./cmd/devenv platform save --force\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress = %q; want %q", progress.String(), want)
	}
	if got := f.calls(t); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, " --from ") }) {
		t.Errorf("smolvm calls = %q; want no restore", got)
	}
}

func TestPlatformStartsColdWithoutACheckpointOrWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name       string
		checkpoint bool
		cold       bool
	}{
		{name: "no checkpoint"},
		{name: "--cold", checkpoint: true, cold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSmolvm(t, vm(""))
			fakeHost(t, f)
			want := "] platform: cold start, because --cold asked for one\n"
			if tc.checkpoint {
				fakeCheckpoint(t, f)
			} else {
				in, err := hostPlatform()
				if err != nil {
					t.Fatal(err)
				}
				want = "] platform: cold start, because no platform checkpoint " + f.o.platformCache().checkpoint(in.key()) +
					"; save one with: go run ./cmd/devenv platform save\n"
			}
			t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
			var progress bytes.Buffer
			f.o.Progress, f.o.Cold = &progress, tc.cold
			e := f.open(t)

			_ = e.platform(t.Context(), "", func() {})

			if !strings.Contains(progress.String(), want) {
				t.Errorf("progress = %q; want %q", progress.String(), want)
			}
			if got := f.calls(t); slices.ContainsFunc(got, func(c string) bool { return strings.Contains(c, " --from ") }) {
				t.Errorf("smolvm calls = %q; want no restore", got)
			}
		})
	}
}

func TestBringUpBuildsNothingWhileAColdVMBoots(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	t.Setenv("FAKE_SMOLVM_FAIL", "machine start")
	var progress bytes.Buffer
	f.o.Progress, f.o.Cold = &progress, true
	e := f.open(t)

	if err := e.bringUp(t.Context(), ""); err == nil {
		t.Fatal("bringUp() succeeded")
	}

	if strings.Contains(progress.String(), "build") {
		t.Errorf("progress = %q; want no build before the VM boots", progress.String())
	}
}

func TestPlatformLetsTheBuildStartBeforeARestoreAndAfterAColdBoot(t *testing.T) {
	for name, cold := range map[string]bool{"restore": false, "cold": true} {
		t.Run(name, func(t *testing.T) {
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
	if strings.Contains(progress.String(), "cold start") || strings.Contains(progress.String(), "interrupted") {
		t.Errorf("progress = %q; want neither a cold start nor a wait for smolvm, which never ran", progress.String())
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

func TestRestoredGatesWaitForEveryLeaseToBeRenewedAfterTheRestore(t *testing.T) {
	restored := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	fresh := restored.Add(time.Second)
	leases := func(renewed map[string]time.Time) []runtime.Object {
		var objs []runtime.Object
		for _, l := range []struct{ namespace, name string }{
			{"kube-node-lease", "node"}, {"kube-system", "kube-controller-manager"}, {"kube-system", "kube-scheduler"},
			{"capi-system", "capi"}, {"capi-kubeadm-bootstrap-system", "bootstrap"},
			{"capi-kubeadm-control-plane-system", "control-plane"}, {"capd-system", "capd"},
		} {
			at, ok := renewed[l.namespace]
			if !ok {
				at = fresh
			}
			objs = append(objs, &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Namespace: l.namespace, Name: l.name},
				Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("holder"), RenewTime: &metav1.MicroTime{Time: at}},
			})
		}
		return objs
	}
	for _, tc := range []struct {
		name    string
		renewed map[string]time.Time
		stale   string
	}{
		{"a node lease from the capture", map[string]time.Time{"kube-node-lease": restored.Add(-time.Hour)}, "kube-node-lease/node"},
		{"a CAPD lease from the capture", map[string]time.Time{"capd-system": restored.Add(-time.Hour)}, "capd-system/capd"},
		{"a lease renewed 3 s before the restore", map[string]time.Time{"kube-node-lease": restored.Add(-3 * time.Second)}, "kube-node-lease/node"},
		{"every lease renewed after the restore", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeSmolvm(t, vm("")).open(t)
			cs := kubefake.NewClientset(leases(tc.renewed)...)
			kubeClient = func(string) (kubernetes.Interface, error) { return cs, nil }
			t.Cleanup(func() { kubeClient = kube.Client })
			warmGatesTimeout = 300 * time.Millisecond
			t.Cleanup(func() { warmGatesTimeout = 2 * time.Minute })

			err := e.restoredGates(t.Context(), restored)

			leasesStale := err != nil && strings.Contains(err.Error(), "leases not renewed since")
			if tc.stale == "" && leasesStale {
				t.Errorf("err = %v; want the lease gates to pass", err)
			}
			if tc.stale != "" && (!leasesStale || !strings.Contains(err.Error(), tc.stale)) {
				t.Errorf("err = %v; want it to name %s", err, tc.stale)
			}
		})
	}
}
