// Command devenv runs oldctl, a legacy controller, in a Cluster API development environment.
// It lives in its own module so the tool's dependencies don't change oldctl's.
package main

import (
	"context"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
)

func main() {
	cli.Main(devenv.Config{
		Root:     os.Getenv("DEVENV_ROOT"),
		Commands: []string{"./cmd/manager"},
		Packages: []devenv.Package{{Name: "manager", RefName: "manager.oldctl.example.com", Config: "config/manager", Images: []string{"manager"}}},
		Test: func(ctx context.Context, e *devenv.Environment) error {
			dyn, err := kube.Dynamic(e.MgmtKubeconfig)
			if err != nil {
				return err
			}
			for _, v := range []string{"v1beta1", "v1beta2"} {
				gvr := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: v, Resource: "clusters"}
				l, err := dyn.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{})
				n := 0
				if l != nil {
					n = len(l.Items)
				}
				fmt.Fprintf(os.Stderr, "OBS-HOOK: list clusters.%s.cluster.x-k8s.io: %d, err=%v\n", v, n, err)
			}
			return nil
		},
	})
}
