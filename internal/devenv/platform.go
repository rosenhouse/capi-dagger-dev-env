package devenv

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// boot creates the VM while it fills the download cache.
func (e *Environment) boot(ctx context.Context, leftover smolvm.State) error {
	downloads, err := e.downloads()
	if err != nil {
		return err
	}
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

func (e *Environment) downloads() ([]infra.Download, error) {
	downloads, err := infra.Downloads(runtime.GOARCH)
	return append(downloads, platform.Downloads()...), err
}

func (e *Environment) cache() fetch.Cache {
	return fetch.Cache{Dir: filepath.Join(e.cacheDir, "downloads")}
}

// platform brings up everything in the VM that holds no first-party code: dockerd, the session registry,
// the management cluster with kapp-controller, CAPI and CAPD, and the workload cluster.
func (e *Environment) platform(ctx context.Context) error {
	downloads, err := e.downloads()
	if err != nil {
		return err
	}
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
	if err := e.stage("registry", e.startRegistry(ctx)); err != nil {
		return err
	}
	if err := e.stage("management cluster", e.managementCluster(ctx)); err != nil {
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
	if err := e.stage("workload cluster", e.workloadCluster(ctx)); err != nil {
		return err
	}
	return e.stage("workload API", e.workloadAPI(ctx))
}

// createVM deletes a VM that an earlier run left, then creates the environment's VM on free host ports.
func (e *Environment) createVM(ctx context.Context, leftover smolvm.State) error {
	if leftover != "" {
		if err := e.vm.Delete(ctx); err != nil {
			return err
		}
	}
	ports, err := state.FreePorts()
	if err != nil {
		return err
	}
	if err := e.WritePorts(ports); err != nil {
		return err
	}
	e.Ports = ports
	return e.vm.Create(ctx, ports)
}

func (e *Environment) startRegistry(ctx context.Context) func() error {
	return func() error {
		if err := e.vm.StartRegistry(ctx); err != nil {
			return err
		}
		url := fmt.Sprintf("http://localhost:%d/v2/", e.Ports.Registry)
		return ready.Wait(ctx, ready.Gate{
			Name: "registry answers from host", Timeout: time.Minute, Interval: time.Second, Attempt: 5 * time.Second,
			Check: func(ctx context.Context) error { return get(ctx, url) },
		})
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

func (e *Environment) managementCluster(ctx context.Context) func() error {
	return func() error {
		kubeconfig, err := e.vm.CreateManagementCluster(ctx)
		if err != nil {
			return err
		}
		if _, err := e.WriteKubeconfig("mgmt", kubeconfig, e.Ports.MgmtAPI); err != nil {
			return err
		}
		cs, err := kube.Client(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "nodes Ready", Timeout: 3 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error { return kube.NodesReady(ctx, cs) },
		})
	}
}

func (e *Environment) workloadCluster(ctx context.Context) func() error {
	return func() error {
		if err := ready.Wait(ctx, ready.Gate{
			// CAPI's and CAPD's webhooks can refuse connections for a while after clusterctl init returns.
			Name: "workload cluster manifests applied", Timeout: 3 * time.Minute, Interval: 5 * time.Second,
			Check: func(ctx context.Context) error {
				return platform.CreateWorkloadCluster(ctx, e.vm, WorkloadCluster, WorkloadNamespace)
			},
		}); err != nil {
			return err
		}
		dyn, err := kube.Dynamic(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "workload Cluster Available", Timeout: 10 * time.Minute, Interval: 5 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error {
				return kube.ClusterAvailable(ctx, dyn, WorkloadNamespace, WorkloadCluster)
			},
		})
	}
}

// workloadAPI publishes the workload API server to the host and writes its kubeconfig.
func (e *Environment) workloadAPI(ctx context.Context) func() error {
	return func() error {
		if err := e.vm.ForwardWorkloadAPI(ctx, WorkloadCluster); err != nil {
			return err
		}
		mgmt, err := kube.Client(e.MgmtKubeconfig)
		if err != nil {
			return err
		}
		secret, err := mgmt.CoreV1().Secrets(WorkloadNamespace).Get(ctx, WorkloadCluster+"-kubeconfig", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if _, err := e.WriteKubeconfig("workload", secret.Data["value"], e.Ports.WorkloadAPI); err != nil {
			return err
		}
		workload, err := kube.Client(e.WorkloadKubeconfig)
		if err != nil {
			return err
		}
		return ready.Wait(ctx, ready.Gate{
			Name: "workload nodes Ready from host", Timeout: 2 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
			Check: func(ctx context.Context) error { return kube.NodesReady(ctx, workload) },
		})
	}
}
