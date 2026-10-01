package devenv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/fetch"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/platform"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/smolvm"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// apiAttempt bounds each check of a gate that calls an API server through a published port,
// because smolvm accepts a connection before anything in the guest answers it.
const apiAttempt = 30 * time.Second

// platform brings up everything in the VM that holds no first-party code: dockerd, the environment's registry,
// the management cluster with kapp-controller, CAPI and CAPD, and the workload cluster. It restores them
// from this host's platform checkpoint if there is one, and else, or if the restore fails, brings them up cold.
// It calls canBuild once the build may use every CPU: at once for a restore, which mostly waits on the disk,
// or once a cold VM has booted.
func (e *Environment) platform(ctx context.Context, leftover smolvm.State, canBuild func()) error {
	checkpoint, err := e.platformCheckpoint()
	if err == nil {
		e.progress("platform: restoring " + checkpoint)
		canBuild()
		err = e.warmPlatform(ctx, leftover, checkpoint)
		if err == nil || ctx.Err() != nil {
			return err
		}
		err = fmt.Errorf("restoring it failed: %w", err)
		machines, listErr := e.opts.SmolVM.List(ctx)
		if listErr != nil {
			return errors.Join(err, listErr)
		}
		if leftover = e.VMState(machines); leftover != "" {
			logs := filepath.Join(e.Dir, "logs", "restore")
			if logErr := e.vm.ExportConsoleLog(ctx, logs); logErr == nil {
				err = fmt.Errorf("%w\nlogs: %s", err, logs)
			}
		}
	}
	e.progress(fmt.Sprintf("platform: cold start, because %v", err))
	return e.coldPlatform(ctx, leftover, canBuild)
}

var errColdAsked = errors.New("--cold asked for one")

// platformCheckpoint returns this host's platform checkpoint, or an error that says why there is none.
func (e *Environment) platformCheckpoint() (string, error) {
	if e.opts.Cold {
		return "", errColdAsked
	}
	in, err := hostPlatform()
	if err != nil {
		return "", err
	}
	return e.opts.platformCache().lookup(in.key())
}

// warmPlatform restores the VM from a platform checkpoint, on fresh host ports, and waits for the platform's gates.
func (e *Environment) warmPlatform(ctx context.Context, leftover smolvm.State, checkpoint string) error {
	if leftover != "" {
		if err := e.vm.Delete(ctx); err != nil {
			return err
		}
	}
	if err := e.stage("restore VM", func() error { return e.vm.Restore(ctx, checkpoint) }); err != nil {
		return err
	}
	if err := e.stage("start VM", func() error { return e.startRestored(ctx, checkpoint) }); err != nil {
		return err
	}
	if err := e.stage("kubeconfigs", func() error { return e.writeKubeconfigs(ctx) }); err != nil {
		return err
	}
	return e.stage("platform gates", func() error { return e.platformGates(ctx) })
}

// startRestored publishes the restored VM's guest ports on free host ports instead of the captured ones, and starts it.
// It retries a start once, because smolvm 1.22.0 gives a restored VM a fixed 30 s to answer.
func (e *Environment) startRestored(ctx context.Context, checkpoint string) error {
	captured, err := smolvm.CheckpointPorts(checkpoint)
	if err != nil {
		return err
	}
	return e.onFreePorts(ctx, func(ports state.Ports) error {
		if err := e.opts.SmolVM.RebindPorts(ctx, e.vm.Name, captured, infra.MachineConfig(ports).Ports); err != nil {
			return err
		}
		err := e.opts.SmolVM.Start(ctx, e.vm.Name, infra.StartOptions(false))
		if smolvm.AgentTimedOut(err) {
			e.progress("the restored VM did not answer in time; starting it again")
			err = e.opts.SmolVM.Start(ctx, e.vm.Name, infra.StartOptions(false))
		}
		return err
	})
}

// onFreePorts records free host ports for the VM, and calls start with them while it holds a host-wide lock, which
// keeps other environments from taking the same ports meanwhile. The lock also serializes the first start after
// installing smolvm 1.22.0, which expands its disk templates through one fixed scratch file.
func (e *Environment) onFreePorts(ctx context.Context, start func(state.Ports) error) error {
	unlock, err := state.WaitLock(ctx, e.startLock())
	if err != nil {
		return err
	}
	defer unlock()
	ports, err := state.FreePorts()
	if err != nil {
		return err
	}
	if err := e.WritePorts(ports); err != nil {
		return err
	}
	e.Ports = ports
	return start(ports)
}

// coldPlatform brings up the platform in a new VM, after it fills the download cache.
func (e *Environment) coldPlatform(ctx context.Context, leftover smolvm.State, booted func()) error {
	downloads, err := downloads()
	if err != nil {
		return err
	}
	if err := e.boot(ctx, leftover, downloads); err != nil {
		return err
	}
	booted()
	if err := e.stage("guest tools", func() error {
		if err := e.vm.Copy(ctx, e.cache(), downloads); err != nil {
			return err
		}
		return e.vm.Install(ctx)
	}); err != nil {
		return err
	}
	if err := e.stage("docker daemon", func() error { return e.vm.StartDocker(ctx) }); err != nil {
		return err
	}
	if err := e.stage("images", func() error { return e.vm.PullImages(ctx) }); err != nil {
		return err
	}
	if err := e.stage("registry", func() error { return e.startRegistry(ctx) }); err != nil {
		return err
	}
	if err := e.stage("management cluster", func() error { return e.managementCluster(ctx) }); err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return e.stage("kapp-controller", func() error { return platform.InstallKappController(gctx, e.vm) })
	})
	g.Go(func() error {
		return e.stage("cluster api", func() error { return platform.InstallClusterAPI(gctx, e.vm) })
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if err := e.stage("workload cluster", func() error { return e.workloadCluster(ctx) }); err != nil {
		return err
	}
	if err := e.stage("workload API", func() error { return e.workloadAPI(ctx) }); err != nil {
		return err
	}
	return e.stage("platform gates", func() error { return e.platformGates(ctx) })
}

// boot creates the VM while it fills the download cache.
func (e *Environment) boot(ctx context.Context, leftover smolvm.State, downloads []infra.Download) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.stage("VM", func() error { return e.createVM(gctx, leftover) }) })
	g.Go(func() error {
		return e.stage("downloads", func() error {
			g, ctx := errgroup.WithContext(gctx)
			for _, d := range downloads {
				g.Go(func() error { _, err := e.cache().Get(ctx, d.File); return err })
			}
			return g.Wait()
		})
	})
	return g.Wait()
}

// downloads are the files that the guest needs, for a guest of the host's architecture.
func downloads() ([]infra.Download, error) {
	downloads, err := infra.Downloads(runtime.GOARCH)
	return append(downloads, platform.Downloads()...), err
}

func (e *Environment) startLock() string { return filepath.Join(e.opts.CacheDir, "start.lock") }

func (e *Environment) cache() fetch.Cache {
	return fetch.Cache{Dir: filepath.Join(e.opts.CacheDir, "downloads")}
}

// createVM deletes a VM that an earlier run left, then creates and starts the environment's VM on free host ports.
func (e *Environment) createVM(ctx context.Context, leftover smolvm.State) error {
	return e.onFreePorts(ctx, func(ports state.Ports) error {
		if leftover != "" {
			if err := e.vm.Delete(ctx); err != nil {
				return err
			}
		}
		return e.vm.Create(ctx, ports, e.branchable)
	})
}

func (e *Environment) startRegistry(ctx context.Context) error {
	if err := e.vm.StartRegistry(ctx); err != nil {
		return err
	}
	return e.wait(ctx, e.registryGate())
}

func (e *Environment) registryGate() ready.Gate {
	url := fmt.Sprintf("http://localhost:%d/v2/", e.Ports.Registry)
	return ready.Gate{
		Name: "registry answers from host", Timeout: time.Minute, Interval: time.Second, Attempt: 5 * time.Second,
		Check: func(ctx context.Context) error { return get(ctx, url) },
	}
}

func get(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}

func (e *Environment) managementCluster(ctx context.Context) error {
	kubeconfig, err := e.vm.CreateManagementCluster(ctx)
	if err != nil {
		return err
	}
	if _, err := e.WriteKubeconfig("mgmt", kubeconfig, e.Ports.MgmtAPI); err != nil {
		return err
	}
	return e.wait(ctx, e.nodesReadyGate("management nodes Ready", e.MgmtKubeconfig, 3*time.Minute))
}

func (e *Environment) nodesReadyGate(name, kubeconfig string, timeout time.Duration) ready.Gate {
	return ready.Gate{
		Name: name, Timeout: timeout, Interval: 2 * time.Second, Attempt: apiAttempt,
		Check: func(ctx context.Context) error {
			cs, err := kube.Client(kubeconfig)
			if err != nil {
				return err
			}
			return kube.NodesReady(ctx, cs)
		},
	}
}

func (e *Environment) workloadCluster(ctx context.Context) error {
	if err := e.wait(ctx, ready.Gate{
		// CAPI's and CAPD's webhooks can refuse connections for a while after clusterctl init returns.
		Name: "workload cluster manifests applied", Timeout: 3 * time.Minute, Interval: 5 * time.Second, Attempt: time.Minute,
		Check: func(ctx context.Context) error {
			return platform.CreateWorkloadCluster(ctx, e.vm, WorkloadCluster, WorkloadNamespace)
		},
	}); err != nil {
		return err
	}
	return e.wait(ctx, e.clusterAvailableGate(10*time.Minute))
}

func (e *Environment) clusterAvailableGate(timeout time.Duration) ready.Gate {
	return ready.Gate{
		Name: "workload Cluster Available", Timeout: timeout, Interval: 5 * time.Second, Attempt: apiAttempt,
		Check: func(ctx context.Context) error {
			dyn, err := kube.Dynamic(e.MgmtKubeconfig)
			if err != nil {
				return err
			}
			return kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster)
		},
	}
}

// workloadAPI publishes the workload API server to the host and writes the kubeconfigs.
func (e *Environment) workloadAPI(ctx context.Context) error {
	if err := e.vm.ForwardWorkloadAPI(ctx, WorkloadCluster, WorkloadNamespace); err != nil {
		return err
	}
	return e.writeKubeconfigs(ctx)
}

// writeKubeconfigs reads both clusters' kubeconfigs from the guest, and writes them for the host's ports.
func (e *Environment) writeKubeconfigs(ctx context.Context) error {
	mgmt, workload, err := e.vm.Kubeconfigs(ctx)
	if err != nil {
		return err
	}
	if _, err := e.WriteKubeconfig("mgmt", mgmt, e.Ports.MgmtAPI); err != nil {
		return err
	}
	_, err = e.WriteKubeconfig("workload", workload, e.Ports.WorkloadAPI)
	return err
}

// platformGates wait until the host reaches the registry and both API servers, both clusters' nodes are Ready,
// the workload Cluster is Available, and kapp-controller serves its Package API.
func (e *Environment) platformGates(ctx context.Context) error {
	for _, g := range []ready.Gate{
		e.registryGate(),
		e.nodesReadyGate("management nodes Ready", e.MgmtKubeconfig, 3*time.Minute),
		e.nodesReadyGate("workload nodes Ready from host", e.WorkloadKubeconfig, 3*time.Minute),
		e.clusterAvailableGate(3 * time.Minute),
		{
			Name: "kapp-controller serves Packages", Timeout: 3 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error {
				dyn, err := kube.Dynamic(e.MgmtKubeconfig)
				if err != nil {
					return err
				}
				return kube.PackagesServed(ctx, dyn)
			},
		},
	} {
		if err := e.wait(ctx, g); err != nil {
			return err
		}
	}
	return nil
}
