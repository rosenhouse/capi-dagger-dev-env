package devenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// The code that shapes the platform: the packages that run the VM and install the platform,
// and devenv's own files that bring it up cold and capture it.
var (
	platformPackages = []string{"infra", "platform", "smolvm"}
	platformFiles    = []string{"platform.go", "capture.go"}
)

func TestPlatformKeyHashesTheCodeThatShapesThePlatform(t *testing.T) {
	in, err := describePlatform("exact-v1-ab")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{}
	hash := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		want[filepath.ToSlash(path)] = hex.EncodeToString(sum[:])
	}
	for _, dir := range platformPackages {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && !strings.HasSuffix(entry.Name(), "_test.go") {
				hash(filepath.Join(dir, entry.Name()))
			}
		}
	}
	for _, file := range platformFiles {
		hash(file)
	}
	if !maps.Equal(in.Sources, want) {
		t.Errorf("sources\n%s\nwant\n%s", marshal(t, in.Sources), marshal(t, want))
	}
	if in.Contract != "exact-v1-ab" || in.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("inputs = %s", marshal(t, in))
	}
}

func TestPlatformKeyChangesWithEveryInput(t *testing.T) {
	in, err := describePlatform("exact-v1-ab")
	if err != nil {
		t.Fatal(err)
	}
	base := in.key()
	edits := map[string]func(*platformInputs){
		"contract": func(in *platformInputs) { in.Contract = smolvm.UnknownContract },
		"platform": func(in *platformInputs) { in.Platform = "darwin/arm64" },
	}
	for path := range in.Sources {
		edits[path] = func(in *platformInputs) { in.Sources[path] = "0" }
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

func TestPlatformCacheSaveMakesAnEntryOnceTheCaptureSucceeds(t *testing.T) {
	c := platformCache{dir: filepath.Join(t.TempDir(), "platform")}
	in, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}
	var tmp string

	var before time.Time

	path, err := c.save(in, func(file string) error {
		tmp = file
		if _, _, err := c.lookup(in.key()); err == nil {
			t.Error("the entry exists during the capture")
		}
		before = time.Now()
		return os.WriteFile(file, []byte("checkpoint"), 0o600)
	})

	if err != nil || path != c.checkpoint(in.key()) {
		t.Fatalf("save() = %q, %v", path, err)
	}
	if got, err := os.ReadFile(path); string(got) != "checkpoint" {
		t.Errorf("checkpoint = %q, %v", got, err)
	}
	var entry platformEntry
	if data, err := os.ReadFile(filepath.Join(c.dir, in.key()+".json")); err != nil || json.Unmarshal(data, &entry) != nil || marshal(t, entry.Inputs) != marshal(t, in) {
		t.Errorf("entry = %s, %v", data, err)
	}
	if found, captured, err := c.lookup(in.key()); err != nil || found != path || captured.Before(before) || captured.After(time.Now()) {
		t.Errorf("lookup() = %s, %v, %v; want %s captured after %v", found, captured, err, path, before)
	}
	if !strings.HasSuffix(tmp, ".checkpoint") || filepath.Dir(tmp) != c.dir {
		t.Errorf("captured to %s; want a .checkpoint file in %s, for smolvm and an atomic rename", tmp, c.dir)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v", tmp, err)
	}
}

func TestPlatformCacheSaveRemovesEveryOtherEntry(t *testing.T) {
	c := platformCache{dir: filepath.Join(t.TempDir(), "platform")}
	in, err := describePlatform("c")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"old.checkpoint", "old.json", ".failed.checkpoint", "restoring/devenv-x.checkpoint"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(c.dir, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.dir, file), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := c.save(in, func(file string) error { return os.WriteFile(file, nil, 0o600) }); err != nil {
		t.Fatal(err)
	}

	var left []string
	if err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left = append(left, strings.TrimPrefix(path, c.dir+"/"))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{in.key() + ".checkpoint", in.key() + ".json", "restoring/devenv-x.checkpoint"}; !slices.Equal(left, want) {
		t.Errorf("cache holds %q; want %q", left, want)
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

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSavePlatformDeletesTheVMItCaptured(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	bringUpPlatform = func(e *Environment, ctx context.Context, _ smolvm.State, _ func(), _ bool) error {
		return e.opts.SmolVM.Create(ctx, e.VM(), smolvm.MachineConfig{})
	}
	capturePlatform = func(_ *Environment, _ context.Context, file string) error {
		writeCheckpoint(t, file, captured...)
		return nil
	}
	t.Cleanup(func() { bringUpPlatform, capturePlatform = (*Environment).coldPlatform, (*Environment).capture })

	if _, err := SavePlatform(t.Context(), f.o, false); err != nil {
		t.Fatal(err)
	}

	if machines := f.machines(); len(machines) > 0 {
		t.Errorf("machines = %+v; want the captured VM deleted", machines)
	}
}

func TestSavePlatformWaitsForARestoreBeforeReplacingCheckpoints(t *testing.T) {
	f := fakeSmolvm(t, vm(""))
	fakeHost(t, f)
	old := fakeCheckpoint(t, f)
	before, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	bringUpPlatform = func(*Environment, context.Context, smolvm.State, func(), bool) error { return nil }
	capturePlatform = func(_ *Environment, _ context.Context, file string) error {
		writeCheckpoint(t, file, captured...)
		return nil
	}
	t.Cleanup(func() { bringUpPlatform, capturePlatform = (*Environment).coldPlatform, (*Environment).capture })
	unlock, err := state.TryLock(filepath.Join(f.o.CacheDir, "restore.lock"))
	if err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() { _, err := SavePlatform(context.Background(), f.o, true); saved <- err }()

	select {
	case err := <-saved:
		t.Fatalf("SavePlatform returned %v while another environment restored", err)
	case <-time.After(500 * time.Millisecond):
	}
	if now, err := os.Stat(old); err != nil || !os.SameFile(before, now) {
		t.Errorf("%s was replaced (%v) while another environment restored", old, err)
	}
	unlock()
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
}
