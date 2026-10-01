package infra

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExportLogs writes smolvm's console log of the VM to dir on the host. If the guest still answers, it adds
// the guest's memory and kernel log, dockerd's log, the CAPI and package resources, events and pods of both
// clusters, the workload Cluster's conditions, and the logs of every Kind and CAPD cluster.
func (v *VM) ExportLogs(ctx context.Context, dir, cluster, namespace string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := v.CLI.DataDir(ctx, v.Name)
	if err == nil {
		err = copyFile(filepath.Join(data, "agent-console.log"), filepath.Join(dir, "agent-console.log"))
	}
	return errors.Join(err, v.exportGuestLogs(ctx, dir, cluster, namespace))
}

// collectLogs writes logs to the current directory. With "api", it writes what the API servers say, cheapest first,
// and bounds each call. With "kind", it exports the logs of every Kind and CAPD cluster.
const collectLogs = `if [ "$1" = kind ]; then
  for c in $(kind get clusters 2>/dev/null); do timeout 60 kind export logs "$c" --name "$c" >/dev/null 2>&1; done
  exit
fi
cp /var/log/dockerd.log . 2>/dev/null
free -m >free.txt 2>&1
dmesg >dmesg.txt 2>&1
docker ps -a >docker-ps.txt 2>&1
k() { kubectl --request-timeout=10s "$@"; }
if k get --raw=/readyz >/dev/null 2>&1; then
  k get pods -A -o wide >pods.txt 2>&1
  k get events -A --sort-by=.lastTimestamp >events.txt 2>&1
  for r in clusters machinedeployments machinesets machines kubeadmcontrolplanes kubeadmconfigs machinehealthchecks \
      clusterresourcesets clusterresourcesetbindings devclusters devmachines packageinstalls apps; do
    k get "$r" -A -o yaml
  done >resources.yaml 2>&1
  timeout 30 clusterctl describe cluster "$CLUSTER" -n "$NAMESPACE" --show-conditions=all >clusterctl-describe.txt 2>&1
  if timeout 30 clusterctl get kubeconfig "$CLUSTER" -n "$NAMESPACE" >/tmp/workload.kubeconfig 2>/dev/null; then
    k --kubeconfig /tmp/workload.kubeconfig get pods -A -o wide >workload-pods.txt 2>&1
    k --kubeconfig /tmp/workload.kubeconfig get events -A --sort-by=.lastTimestamp >workload-events.txt 2>&1
  fi
fi
`

func (v *VM) exportGuestLogs(ctx context.Context, dir, cluster, namespace string) error {
	if err := v.WriteFile(ctx, "/tmp/devenv-collect-logs.sh", []byte(collectLogs)); err != nil {
		return err
	}
	if err := v.Run(ctx, fmt.Sprintf(`rm -rf /tmp/devenv-logs && mkdir /tmp/devenv-logs && cd /tmp/devenv-logs
CLUSTER=%s NAMESPACE=%s CLUSTERCTL_DISABLE_VERSIONCHECK=true timeout 90 sh /tmp/devenv-collect-logs.sh api || true
timeout 120 sh /tmp/devenv-collect-logs.sh kind || true
tar -czf /tmp/devenv-logs.tgz .`, cluster, namespace)); err != nil {
		return err
	}
	archive := filepath.Join(dir, ".guest-logs.tgz")
	defer os.Remove(archive)
	if err := v.CLI.CopyOut(ctx, v.Name, "/tmp/devenv-logs.tgz", archive); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	return untar(f, dir)
}

// untar extracts the gzipped tar archive r into dir.
func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(h.Name) {
			return fmt.Errorf("archive entry %q is outside %s", h.Name, dir)
		}
		path := filepath.Join(dir, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeFile(path, tr)
		}
		if err != nil {
			return err
		}
	}
}

func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return errors.Join(err, f.Close())
}

func copyFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeFile(dst, f)
}
