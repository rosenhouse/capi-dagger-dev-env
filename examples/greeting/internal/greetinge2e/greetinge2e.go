// Package greetinge2e checks, from the host, that the Greeting example works in an environment.
package greetinge2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
	demov1 "github.com/rosenhouse/capi-dagger-dev-env/examples/greeting/api/v1alpha1"
)

const (
	greetingName = "e2e"
	// workloadNamespace is where greeting-syncer copies Greetings, its --target-namespace default,
	// and so where greeting-controller deploys hello and nginx.
	workloadNamespace = "default"
)

// GreetingReachesWorkloadCluster sets a Greeting for cluster in namespace of the management cluster, then waits for
// hello, behind nginx in the workload cluster, to serve it. It does this twice to cover updates.
func GreetingReachesWorkloadCluster(ctx context.Context, mgmtKubeconfig, workloadKubeconfig, namespace, cluster string) error {
	mgmt, err := controllerRuntimeClient(mgmtKubeconfig)
	if err != nil {
		return err
	}
	workload, err := kube.Client(workloadKubeconfig)
	if err != nil {
		return err
	}
	for _, message := range []string{"hello from " + cluster, "updated hello from " + cluster} {
		g := &demov1.Greeting{}
		g.Name, g.Namespace = greetingName, namespace
		if _, err := controllerutil.CreateOrUpdate(ctx, mgmt, g, func() error {
			g.Spec = demov1.GreetingSpec{ClusterName: cluster, Message: message}
			return nil
		}); err != nil {
			return err
		}
		if err := ready.Wait(ctx, ready.Gate{
			Name: fmt.Sprintf("workload cluster serves %q", message), Timeout: 5 * time.Minute, Interval: 3 * time.Second,
			Check: func(ctx context.Context) error { return serves(ctx, workload, message) },
		}); err != nil {
			return err
		}
	}
	return nil
}

// HelloServesVersion waits for the workload cluster to serve the scenario's Greeting from a hello built as version.
// It needs GreetingReachesWorkloadCluster to have run.
func HelloServesVersion(ctx context.Context, workloadKubeconfig, version string) error {
	workload, err := kube.Client(workloadKubeconfig)
	if err != nil {
		return err
	}
	return ready.Wait(ctx, ready.Gate{
		Name: fmt.Sprintf("workload cluster serves hello %s", version), Timeout: 5 * time.Minute, Interval: 3 * time.Second,
		Check: func(ctx context.Context) error {
			body, err := get(ctx, workload)
			if err != nil {
				return err
			}
			if !strings.HasSuffix(body, "(hello "+version+")\n") {
				return fmt.Errorf("proxy served %q", body)
			}
			return nil
		},
	})
}

// serves checks the greeting through the proxy Service.
func serves(ctx context.Context, cs kubernetes.Interface, message string) error {
	body, err := get(ctx, cs)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(body, message+" (hello ") {
		return fmt.Errorf("proxy served %q", body)
	}
	return nil
}

// get reads the proxy Service's response by way of the API server's service proxy.
func get(ctx context.Context, cs kubernetes.Interface) (string, error) {
	body, err := cs.CoreV1().Services(workloadNamespace).ProxyGet("http", greetingName+"-proxy", "80", "/", nil).DoRaw(ctx)
	return string(body), err
}

func controllerRuntimeClient(kubeconfig string) (client.Client, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := demov1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}
