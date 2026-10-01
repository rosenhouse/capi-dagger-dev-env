package infra

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestDownloadsPinEveryToolForEachArchitecture(t *testing.T) {
	urls := map[string]bool{}
	for _, arch := range []string{"amd64", "arm64"} {
		downloads, err := Downloads(arch)
		if err != nil {
			t.Fatal(err)
		}
		if len(downloads) != len(tools) {
			t.Errorf("%s: %d downloads, want %d", arch, len(downloads), len(tools))
		}
		paths := map[string]bool{}
		for _, d := range downloads {
			if !sha256Hex.MatchString(d.SHA256) || strings.Contains(d.URL, "%!") || paths[d.Path] || urls[d.URL] {
				t.Errorf("%s: %+v is unpinned, malformed or repeated", arch, d)
			}
			paths[d.Path], urls[d.URL] = true, true
		}
	}
}

func TestDownloadsRejectOtherArchitectures(t *testing.T) {
	if _, err := Downloads("riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("err = %v", err)
	}
}

func TestZeroFillStepsKeepMemoryFreeOnTheHostAndInTheGuest(t *testing.T) {
	for _, tc := range []struct{ hostMiB, guestMiB, want int }{
		{hostMiB: 16000, guestMiB: 2600, want: 4},
		{hostMiB: 3100, guestMiB: 4096, want: 2},
		{hostMiB: 2000, guestMiB: 4096, want: 0},
		{hostMiB: 16000, guestMiB: 400, want: 0},
	} {
		if got := zeroFillSteps(tc.hostMiB, tc.guestMiB); got != tc.want {
			t.Errorf("zeroFillSteps(%d, %d) = %d; want %d", tc.hostMiB, tc.guestMiB, got, tc.want)
		}
	}
}

func TestPullRetriesAStalledPull(t *testing.T) {
	t.Parallel()
	calls, out, err := runPull(t, "exec sleep 60", "echo 1")

	if err == nil || !strings.Contains(out, "could not pull kindest/node") {
		t.Errorf("err = %v, output %q", err, out)
	}
	if want := strings.Repeat("pull -q kindest/node\n", 3); calls != want {
		t.Errorf("docker calls = %q; want %q", calls, want)
	}
}

func TestPullLetsASlowPullProgress(t *testing.T) {
	t.Parallel()
	calls, out, err := runPull(t, "sleep 3", `n=$(cat "$DIR/size" 2>/dev/null || echo 0); echo $((n + 1)) | tee "$DIR/size"`)

	if err != nil || calls != "pull -q kindest/node\n" {
		t.Errorf("err = %v, output %q, docker calls %q; want one pull", err, out, calls)
	}
}

// runPull runs pullScript for one image, with a stall of 1 s, and with docker and du run as the given scripts.
// It returns docker's arguments, a line per call, and the script's output.
func runPull(t *testing.T, docker, du string) (calls, out string, err error) {
	t.Helper()
	dir := t.TempDir()
	for name, script := range map[string]string{"docker": `echo "$@" >>"$DIR/calls"` + "\n" + docker, "du": du} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-eu", "-c", pullScript([]string{"kindest/node"}, 1, 1))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "DIR="+dir)
	output, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return string(data), string(output), err
}

func TestHostsTOMLPullsFromTheRegistryOverPlainHTTP(t *testing.T) {
	got := hostsTOML("172.31.255.254:5000")
	want := `server = "http://172.31.255.254:5000"

[host."http://172.31.255.254:5000"]
  capabilities = ["pull", "resolve"]
`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestUntarExtractsFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	archive := tgz(t, map[string]string{"mgmt/": "", "mgmt/kubelet.log": "started", "resources.yaml": "kind: Cluster"})

	if err := untar(bytes.NewReader(archive), dir); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{"mgmt/kubelet.log": "started", "resources.yaml": "kind: Cluster"} {
		if got, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
}

func TestUntarRejectsPathsOutsideTheDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "logs")

	err := untar(bytes.NewReader(tgz(t, map[string]string{"../escaped": "x"})), dir)

	if err == nil {
		t.Error("no error")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); err == nil {
		t.Error("wrote outside the directory")
	}
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
