// Command devenv runs the Greeting example in a Cluster API development environment.
package main

import (
	"context"
	"io"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/greetinge2e"
)

func main() { cli.Main(config) }

var config = devenv.Config{
	Commands: []string{"./cmd/addon-manager", "./cmd/greeting-syncer", "./cmd/greeting-controller", "./cmd/hello"},
	Packages: []devenv.Package{
		{Name: "addon-manager", RefName: "addon-manager.demo.example.com", Config: "config/addon-manager", Images: []string{"addon-manager"}},
		{Name: "greeting-syncer", RefName: "greeting-syncer.demo.example.com", Config: "config/greeting-syncer", Images: []string{"greeting-syncer"}},
		{Name: "greeting-controller", RefName: "greeting-controller.demo.example.com", Config: "config/greeting-controller", Images: []string{"greeting-controller", "hello"}, On: devenv.Workload},
	},
	Ready: remoteInstall,
	Test:  greeting,
}

// remotePackageInstall is the name addon-manager gives greeting-controller's PackageInstall.
const remotePackageInstall = devenv.WorkloadCluster + "-greeting-controller"

// remoteInstall waits for addon-manager to install greeting-controller into the workload cluster.
func remoteInstall(ctx context.Context, e *devenv.Environment) error {
	dyn, err := kube.Dynamic(e.MgmtKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: "remote PackageInstall reconciled", Timeout: 5 * time.Minute, Interval: 3 * time.Second,
		Check: func(ctx context.Context) error {
			return kube.PackageInstallReconciled(ctx, dyn, devenv.WorkloadNamespace, remotePackageInstall)
		},
	})
}

// greeting runs the Greeting scenario, then redeploys hello with a test version and checks that it serves it.
func greeting(ctx context.Context, e *devenv.Environment) error {
	if err := greetinge2e.GreetingReachesWorkloadCluster(ctx, e.MgmtKubeconfig, e.WorkloadKubeconfig, devenv.WorkloadNamespace, devenv.WorkloadCluster); err != nil {
		return err
	}
	if err := e.Redeploy(ctx, "redeploy-test", io.Discard); err != nil {
		return err
	}
	return greetinge2e.HelloServesVersion(ctx, e.WorkloadKubeconfig, "redeploy-test")
}
