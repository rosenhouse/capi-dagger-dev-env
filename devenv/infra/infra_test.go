package infra

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRequireCgroupV2(t *testing.T) {
	if err := requireCgroupV2("63677270\n"); err != nil {
		t.Errorf("rejected cgroup2's filesystem magic: %v", err)
	}
	tmpfs := "1021994\n"
	if err := requireCgroupV2(tmpfs); err == nil || !strings.Contains(err.Error(), "cgroup v2") {
		t.Errorf("err = %v, want an explanation that cgroup v2 is required", err)
	}
}

func TestTailKeepsLastLines(t *testing.T) {
	if got := tail("a\nb\nc\n", 2); got != "b\nc" {
		t.Errorf("tail = %q", got)
	}
	if got := tail("a\n", 2); got != "a" {
		t.Errorf("tail = %q", got)
	}
}

func TestRequireInotifyLimits(t *testing.T) {
	if err := requireInotify("512\n524288\n"); err != nil {
		t.Errorf("enough: %v", err)
	}
	for _, low := range []string{"511\n524288\n", "512\n524287\n"} {
		if err := requireInotify(low); err == nil || !strings.Contains(err.Error(), "at least 512 inotify instances and 524288 watches") {
			t.Errorf("%q: err = %v", low, err)
		}
	}
	if err := requireInotify("garbage"); err == nil {
		t.Error("garbage: no error")
	}
}

// runClusterScript runs managementClusterScript with a fake kind command, whose create fails the first `failures` times.
func runClusterScript(t *testing.T, failures int) (calls string, err error) {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	kind := `#!/bin/sh
cat >/dev/null
echo "kind $*" >>` + log + `
if [ "$1" = create ]; then
	n=$(grep -c "kind create" ` + log + `)
	[ "$n" -gt ` + strconv.Itoa(failures) + ` ]
fi
`
	if err := os.WriteFile(filepath.Join(bin, "kind"), []byte(kind), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", managementClusterScript())
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	err = cmd.Run()
	out, _ := os.ReadFile(log)
	return string(out), err
}

var (
	createCall = "kind create cluster --name mgmt --retain --image " + kindNodeImage + " --config -\n"
	getCall    = "kind get kubeconfig --name mgmt\n"
)

func TestManagementClusterScriptCreatesTheCluster(t *testing.T) {
	calls, err := runClusterScript(t, 0)

	if err != nil || calls != createCall+getCall {
		t.Errorf("err = %v, calls:\n%s", err, calls)
	}
}

func TestManagementClusterScriptFailsWhenCreateFails(t *testing.T) {
	calls, err := runClusterScript(t, 1)

	if err == nil || strings.Count(calls, "kind create") != 1 {
		t.Errorf("err = %v, calls:\n%s", err, calls)
	}
}

// runKubeletDropIn runs kubeletDropIn's ExecStartPre against a fake cgroup tree with procs in its root.
func runKubeletDropIn(t *testing.T, procs string, moveFails bool) (moved string, err error) {
	t.Helper()
	root := t.TempDir()
	scope := filepath.Join(root, "init.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	if moveFails {
		scope = filepath.Join(scope, "cgroup.procs")
		if err := os.Mkdir(scope, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte(procs), 0o644); err != nil {
		t.Fatal(err)
	}
	_, command, ok := strings.Cut(kubeletDropIn, "ExecStartPre=/bin/sh -c ")
	if !ok {
		t.Fatal("no ExecStartPre")
	}
	// systemd unescapes $$ and strips the quotes; the test points the command at the fake tree.
	command = strings.Trim(strings.TrimSpace(command), "'")
	command = strings.ReplaceAll(strings.ReplaceAll(command, "$$", "$"), "/sys/fs/cgroup", root)
	err = exec.Command("sh", "-c", command).Run()
	out, _ := os.ReadFile(filepath.Join(root, "init.scope", "cgroup.procs"))
	return string(out), err
}

func TestKubeletDropInEmptiesTheRootCgroup(t *testing.T) {
	moved, err := runKubeletDropIn(t, "0\n123\n456\n", false)

	if err != nil || moved != "123\n456\n" {
		t.Errorf("moved %q, err %v", moved, err)
	}
}

func TestKubeletDropInToleratesAProcessThatCannotMove(t *testing.T) {
	if _, err := runKubeletDropIn(t, "123\n", true); err != nil {
		t.Errorf("a failed move failed the kubelet's start: %v", err)
	}
}

func TestManagementNodeMountsTheKubeletDropIn(t *testing.T) {
	want := "  - hostPath: " + kubeletDropInPath + "\n    containerPath: /etc/systemd/system/kubelet.service.d/05-devenv-cgroup.conf\n"
	if !strings.Contains(mgmtKindConfig, want) {
		t.Errorf("mgmtKindConfig lacks\n%s", want)
	}
}
