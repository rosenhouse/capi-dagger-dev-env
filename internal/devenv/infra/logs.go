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

// ExportLogs writes smolvm's console log of the VM to dir on the host. If the guest still answers,
// it adds the logs of every Kind and CAPD cluster, the CAPI and package resources, and dockerd's log.
func (v *VM) ExportLogs(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := v.CLI.DataDir(ctx, v.Name)
	if err == nil {
		err = copyFile(filepath.Join(data, "agent-console.log"), filepath.Join(dir, "agent-console.log"))
	}
	return errors.Join(err, v.exportGuestLogs(ctx, dir))
}

func (v *VM) exportGuestLogs(ctx context.Context, dir string) error {
	if err := v.Run(ctx, `rm -rf /tmp/devenv-logs && mkdir /tmp/devenv-logs && cd /tmp/devenv-logs
cp /var/log/dockerd.log . || true
docker ps -a >docker-ps.txt 2>&1 || true
for cluster in $(kind get clusters 2>/dev/null); do kind export logs "$cluster" --name "$cluster" >/dev/null 2>&1 || true; done
for r in clusters machines kubeadmcontrolplanes machinedeployments devclusters devmachines packageinstalls apps; do
  kubectl get "$r" -A -o yaml || true
done >resources.yaml 2>&1
tar -czf /tmp/devenv-logs.tgz .`); err != nil {
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
