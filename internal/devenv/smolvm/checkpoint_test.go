package smolvm_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
)

// epyc7763 is the contract smolvm recorded in a checkpoint captured on the host that
// testdata/cpuinfo-amd-epyc-7763.txt and testdata/manifest-amd-epyc-7763.json describe.
const epyc7763 = "exact-v1-17295093af9c69a6186eb1793c75c1f81f38313bd108183f53e8aa5f7fb0129b"

func TestLinuxAMD64Contract(t *testing.T) {
	amd := read(t, "testdata/cpuinfo-amd-epyc-7763.txt")
	for _, tc := range []struct {
		name, cpuinfo, want string
	}{
		{"AMD", amd, epyc7763},
		{"Intel", read(t, "testdata/cpuinfo-intel-xeon.txt"), "linux-kvm-intel-portable-v1"},
		{"AMD with other spacing", strings.ReplaceAll(amd, " sse ", " \t sse  "), epyc7763},
		{"AMD with another later processor", amd + "processor\t: 2\nvendor_id\t: GenuineIntel\nflags\t\t: fpu\n", epyc7763},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := smolvm.LinuxAMD64Contract(tc.cpuinfo); got != tc.want {
				t.Errorf("got %s; want %s", got, tc.want)
			}
		})
	}
}

func TestLinuxAMD64ContractCoversTheCPUIdentity(t *testing.T) {
	amd := read(t, "testdata/cpuinfo-amd-epyc-7763.txt")
	for _, edit := range [][2]string{
		{"vendor_id\t: AuthenticAMD", "vendor_id\t: HygonGenuine"},
		{"cpu family\t: 25", "cpu family\t: 26"},
		{"model\t\t: 1\n", "model\t\t: 17\n"},
		{"stepping\t: 1", "stepping\t: 2"},
		{" avx2 ", " "},
	} {
		if got := smolvm.LinuxAMD64Contract(strings.Replace(amd, edit[0], edit[1], 1)); got == epyc7763 {
			t.Errorf("replacing %q with %q left the contract unchanged", edit[0], edit[1])
		}
	}
	if got := smolvm.LinuxAMD64Contract(strings.Replace(amd, "cpu MHz\t\t: 3243.874", "cpu MHz\t\t: 1", 1)); got != epyc7763 {
		t.Error("the clock speed changed the contract")
	}
}

func TestHostContract(t *testing.T) {
	got, err := smolvm.HostContract()
	if err != nil {
		t.Fatal(err)
	}
	want := smolvm.UnknownContract
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		want = smolvm.LinuxAMD64Contract(read(t, "/proc/cpuinfo"))
	}
	if got != want {
		t.Errorf("HostContract() = %s; want %s", got, want)
	}
}

func TestCheckpointContract(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, want string
	}{
		{"AMD", read(t, "testdata/manifest-amd-epyc-7763.json"), epyc7763},
		{"Intel", `{"checkpoint": {"cpu_contract": {"kind": "linux-kvm-intel-portable-v1"}}}`, "linux-kvm-intel-portable-v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, checkpoint([]byte("payload"), []byte(tc.manifest)))

			got, err := smolvm.CheckpointContract(path)

			if err != nil || got != tc.want {
				t.Errorf("CheckpointContract() = %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestCheckpointPorts(t *testing.T) {
	path := writeFile(t, checkpoint([]byte("payload"), []byte(read(t, "testdata/manifest-amd-epyc-7763.json"))))

	got, err := smolvm.CheckpointPorts(path)

	if want := []smolvm.Port{{Host: 18080, Guest: 8080}}; err != nil || !slices.Equal(got, want) {
		t.Errorf("CheckpointPorts() = %v, %v; want %v", got, err, want)
	}
}

func TestCheckpointPortsOfACheckpointWithoutNetwork(t *testing.T) {
	path := writeFile(t, checkpoint(nil, []byte(`{"checkpoint": {"cpu_contract": {"kind": "linux-kvm-intel-portable-v1"}}}`)))

	if got, err := smolvm.CheckpointPorts(path); err != nil || len(got) > 0 {
		t.Errorf("CheckpointPorts() = %v, %v; want none", got, err)
	}
}

func TestCheckpointPortsRejectsAPackWithoutACheckpoint(t *testing.T) {
	path := writeFile(t, checkpoint(nil, []byte(`{"mode": "vm"}`)))

	if _, err := smolvm.CheckpointPorts(path); err == nil || !strings.Contains(err.Error(), "a pack without a checkpoint") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckpointContractRejectsMalformedFiles(t *testing.T) {
	good := checkpoint([]byte("payload"), []byte(`{"checkpoint": {"cpu_contract": {"kind": "exact-v1", "fingerprint": "ab"}}}`))
	footer := len(good) - 64
	for _, tc := range []struct {
		name, want string
		file       []byte
	}{
		{"shorter than a footer", "too short", good[footer+1:]},
		{"wrong magic", "not a checkpoint", edit(good, func(b []byte) { copy(b[footer:], "NOTAPACK") })},
		{"bytes after the footer", "not a checkpoint", append(slices.Clone(good), 0)},
		{"manifest longer than the file", "exceeds 16 MiB or the file",
			edit(good, func(b []byte) { binary.LittleEndian.PutUint64(b[footer+44:], 1<<20) })},
		{"manifest over 16 MiB", "exceeds 16 MiB", checkpoint(nil, make([]byte, 16<<20+1))},
		{"manifest not JSON", "unexpected end of JSON input", checkpoint([]byte("payload"), []byte("{"))},
		{"pack without a checkpoint", "a pack without a checkpoint", checkpoint([]byte("payload"), []byte(`{"mode": "vm"}`))},
		{"contract without a kind", "no checkpoint CPU contract", checkpoint(nil, []byte(`{"checkpoint": {"cpu_contract": {}}}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, tc.file)

			got, err := smolvm.CheckpointContract(path)

			if err == nil || !strings.HasPrefix(err.Error(), path+": ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("CheckpointContract() = %q, %v; want an error naming the file and containing %q", got, err, tc.want)
			}
		})
	}
}

func TestCheckpointContractNamesAMissingFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.checkpoint")

	_, err := smolvm.CheckpointContract(path)

	if want := "open " + path + ": no such file or directory"; err == nil || err.Error() != want {
		t.Errorf("error = %v; want %s", err, want)
	}
}

// checkpoint lays out a checkpoint file as crates/smolvm-checkpoint/FORMAT.md §3 in smolvm's source describes.
func checkpoint(payload, manifest []byte) []byte {
	footer := make([]byte, 64)
	copy(footer, "SMOLPACK")
	binary.LittleEndian.PutUint32(footer[8:], 1)
	binary.LittleEndian.PutUint64(footer[28:], uint64(len(payload)))
	binary.LittleEndian.PutUint64(footer[36:], uint64(len(payload)))
	binary.LittleEndian.PutUint64(footer[44:], uint64(len(manifest)))
	return slices.Concat(payload, manifest, footer)
}

func edit(b []byte, f func([]byte)) []byte {
	b = slices.Clone(b)
	f(b)
	return b
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.checkpoint")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
