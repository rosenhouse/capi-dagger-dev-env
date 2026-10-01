package devenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// platform brings up everything in the VM that holds no first-party code: dockerd, the environment's registry,
// the management cluster with kapp-controller, CAPI and CAPD, and the workload cluster. It restores them
// from this host's platform checkpoint if there is one, and else, or if the restore fails, brings them up cold.
// With o.Warm or o.Retain, a restore that fails fails the bring-up instead.
// It calls canBuild once the build may use every CPU: at once for a restore, which mostly waits on the disk,
// or once a cold VM has booted.
func (e *Environment) platform(ctx context.Context, leftover smolvm.State, canBuild func()) error {
	checkpoint, captured, err := e.platformCheckpoint()
	if err != nil {
		if e.opts.Warm {
			return err
		}
		e.progress(fmt.Sprintf("platform: cold start, because %v", err))
		return e.coldPlatform(ctx, leftover, canBuild, false)
	}
	e.progress(fmt.Sprintf("platform: restoring %s, captured %v ago", checkpoint, time.Since(captured).Round(time.Minute)))
	canBuild()
	err = e.warmPlatform(ctx, leftover, checkpoint)
	if err == nil || e.opts.Warm || e.opts.Retain {
		return err
	}
	logs := filepath.Join(e.Dir, "logs", "restore")
	if discardErr := e.discardRestore(ctx, logs); discardErr != nil {
		return errors.Join(err, discardErr)
	}
	e.progress(fmt.Sprintf("platform: cold start, because restoring it failed: %v\nlogs: %s\nIf restores keep failing, replace the checkpoint with: %s",
		err, logs, e.opts.hint("platform save --force")))
	return e.coldPlatform(ctx, "", canBuild, false)
}

// discardRestore keeps the logs of a restored VM in dir, and deletes the VM.
func (e *Environment) discardRestore(ctx context.Context, dir string) error {
	machines, err := e.opts.SmolVM.List(ctx)
	if err != nil {
		return err
	}
	switch e.VMState(machines) {
	case "":
		return nil
	case smolvm.Running:
		exportCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = e.vm.ExportLogs(exportCtx, dir, WorkloadCluster, WorkloadNamespace)
		cancel()
	default:
		err = e.vm.ExportConsoleLog(ctx, dir)
	}
	if err != nil {
		e.progress("export logs: " + err.Error())
	}
	return e.vm.Delete(ctx)
}

var errColdAsked = errors.New("--cold asked for one")

// maxPlatformAge bounds a checkpoint's age well within the 60 days after which cert-manager renews the certificates
// that it issued before the capture. It exceeds a month, so that CI's monthly cache keys expire first.
const maxPlatformAge = 35 * 24 * time.Hour

// platformCheckpoint returns this host's platform checkpoint and when it was captured,
// or an error that says why there is none.
func (e *Environment) platformCheckpoint() (string, time.Time, error) {
	if e.opts.Cold {
		return "", time.Time{}, errColdAsked
	}
	in, err := hostPlatform()
	if err != nil {
		return "", time.Time{}, err
	}
	path, captured, err := e.opts.platformCache().lookup(in.key())
	if errors.Is(err, fs.ErrNotExist) {
		return "", time.Time{}, fmt.Errorf("no platform checkpoint %s; save one with: %s", path, e.opts.hint("platform save"))
	}
	if age := time.Since(captured); err == nil && age > maxPlatformAge {
		err = fmt.Errorf("%s was captured %d days ago; replace it with: %s", path, int(age.Hours()/24), e.opts.hint("platform save --force"))
	}
	return path, captured, err
}

// warmPlatform restores the VM from a platform checkpoint, on fresh host ports, and waits for the platform's gates.
func (e *Environment) warmPlatform(ctx context.Context, leftover smolvm.State, checkpoint string) error {
	if leftover != "" {
		if err := e.vm.Delete(ctx); err != nil {
			return err
		}
	}
	captured, err := e.restoreVM(ctx, checkpoint)
	if err != nil {
		return err
	}
	restored, err := e.startRestored(ctx, captured)
	if err != nil {
		return err
	}
	if err := e.stage("kubeconfigs", func() error { return e.writeKubeconfigs(ctx) }); err != nil {
		return err
	}
	return e.stage("platform gates", func() error { return restoredGates(e, ctx, restored) })
}

// restoreVM creates the VM from a checkpoint while no other environment does, because two at once each take
// about four times as long as one. It returns the host ports that the checkpoint publishes.
// It restores from a link to the checkpoint, so that a save that replaces the checkpoint meanwhile cannot mix two files.
// smolvm fails if the checkpoint's links change while it verifies the checkpoint, so only the restore lock's holder
// links or unlinks it.
func (e *Environment) restoreVM(ctx context.Context, checkpoint string) ([]smolvm.Port, error) {
	unlock, err := e.hostLock(ctx, "restore")
	if err != nil {
		return nil, err
	}
	defer unlock()
	pinned, err := e.opts.platformCache().pin(checkpoint, e.vm.Name)
	if err != nil {
		return nil, err
	}
	defer os.Remove(pinned)
	captured, err := smolvm.CheckpointPorts(pinned)
	if err != nil {
		return nil, err
	}
	return captured, e.stage("restore VM", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		said := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(said)
			e.progress("interrupted; devenv deletes the VM once smolvm has restored it")
		})
		defer func() {
			if !stop() {
				<-said
			}
		}()
		return e.vm.Restore(ctx, pinned)
	})
}

// startRestored publishes the restored VM's guest ports on free host ports instead of the captured ones, starts it,
// and returns when it began.
func (e *Environment) startRestored(ctx context.Context, captured []smolvm.Port) (began time.Time, err error) {
	return began, e.onFreePorts(ctx, "start VM", func(ports state.Ports) error {
		began = time.Now()
		retried, err := e.vm.StartRestored(ctx, captured, ports)
		if retried {
			e.progress("the restored VM did not answer in time, so devenv started it again")
		}
		return err
	})
}

// restoredGates is a variable so that tests can stand in for the clusters.
var restoredGates = (*Environment).restoredGates

// warmGatesTimeout bounds the gates of a restored platform, which pass within seconds unless the restore failed.
var warmGatesTimeout = 2 * time.Minute

// clusterLeases are what kubelets renew every 10 s, and the controller manager and scheduler every 2 s.
var clusterLeases = []kube.Leases{
	{Namespace: "kube-node-lease"},
	{Namespace: "kube-system", Names: []string{"kube-controller-manager", "kube-scheduler"}},
}

// mgmtLeases add the leader leases of CAPI's controllers and CAPD.
var mgmtLeases = slices.Concat(clusterLeases, []kube.Leases{
	{Namespace: "capi-system"},
	{Namespace: "capi-kubeadm-bootstrap-system"},
	{Namespace: "capi-kubeadm-control-plane-system"},
	{Namespace: "capd-system"},
})

// restoredGates first wait until the kubelets and controllers of both clusters renew their leases after restored,
// because the status that a restored cluster reports dates from the capture. Then they wait for the platform gates.
func (e *Environment) restoredGates(ctx context.Context, restored time.Time) error {
	ctx, cancel := context.WithTimeoutCause(ctx, warmGatesTimeout, fmt.Errorf("a restored platform passes its gates within %v", warmGatesTimeout))
	defer cancel()
	// The guest's clock lags the host's by up to a second after a restore.
	since := restored.Add(-2 * time.Second)
	for _, g := range []ready.Gate{
		e.leasesGate("management", e.MgmtKubeconfig, since, mgmtLeases),
		e.leasesGate("workload", e.WorkloadKubeconfig, since, clusterLeases),
	} {
		if err := e.wait(ctx, g); err != nil {
			return err
		}
	}
	return e.platformGates(ctx)
}

// kubeClient is a variable so that tests can stand in for a cluster's API.
var kubeClient = kube.Client

func (e *Environment) leasesGate(cluster, kubeconfig string, since time.Time, leases []kube.Leases) ready.Gate {
	return ready.Gate{
		Name: cluster + " leases renewed since the restore", Timeout: warmGatesTimeout, Interval: 2 * time.Second, Attempt: apiAttempt,
		Check: func(ctx context.Context) error {
			cs, err := kubeClient(kubeconfig)
			if err != nil {
				return err
			}
			return kube.LeasesRenewedSince(ctx, cs, since, leases...)
		},
	}
}
