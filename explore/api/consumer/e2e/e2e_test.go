// Package e2e runs legacyctl's end-to-end tests against one shared devenv environment.
package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"

	"example.com/legacyctl/internal/scenario"
)

var env *devenv.Environment

func obs(format string, args ...any) { fmt.Printf("OBS: "+format+"\n", args...) }

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	obs("TestMain cwd %s", wd)
	cfg := scenario.Config("default")
	start := time.Now()
	e, err := devenv.Up(context.Background(), cfg, devenv.Options{Name: "e2e", StateDir: ".devenv", Progress: os.Stderr})
	if err != nil {
		obs("Up failed after %v: %v", time.Since(start).Round(time.Second), err)
		os.Exit(1)
	}
	obs("Up took %v; env dir %s; kubeconfigs %s %s", time.Since(start).Round(time.Second), e.Dir, e.MgmtKubeconfig, e.WorkloadKubeconfig)
	env = e
	code := m.Run()
	obs("Close: %v", e.Close())
	os.Exit(code)
}

func managerImage(t *testing.T) string {
	cs, err := kube.Client(env.MgmtKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	d, err := cs.AppsV1().Deployments("legacyctl").Get(context.Background(), "manager", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0].Image
}

func TestParallelA(t *testing.T) { t.Parallel(); listNodes(t, env.MgmtKubeconfig) }
func TestParallelB(t *testing.T) { t.Parallel(); listNodes(t, env.WorkloadKubeconfig) }

func listNodes(t *testing.T, kubeconfig string) {
	cs, err := kube.Client(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs("%s: %d nodes", filepath.Base(kubeconfig), len(nodes.Items))
}

func TestStateDirInsideModule(t *testing.T) {
	entries, _ := filepath.Glob(filepath.Join(env.Dir, "*"))
	obs("env dir %s holds %v", env.Dir, entries)
}

// A redeploy with no source change should leave the manager's image alone.
func TestRedeployWithoutSourceChange(t *testing.T) {
	ctx := context.Background()
	before := managerImage(t)
	readyBefore := scenario.ReadyCalls.Load()
	if err := env.Redeploy(ctx, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	after := managerImage(t)
	if err := env.Redeploy(ctx, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	again := managerImage(t)
	obs("redeploy without source change: image changed on 1st: %v, on 2nd: %v", before != after, after != again)
	obs("Ready calls before redeploys %d, after %d", readyBefore, scenario.ReadyCalls.Load())
}

// Parallel tests that each redeploy share one environment.
func TestConcurrentRedeploys(t *testing.T) {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { errs[i] = env.Redeploy(context.Background(), fmt.Sprintf("c%d", i), io.Discard) })
	}
	wg.Wait()
	obs("concurrent redeploys: %v | %v", errs[0], errs[1])
}

// A multi-cluster controller or the CAPI e2e framework reads the workload Cluster's kubeconfig Secret.
func TestWorkloadSecretFromHost(t *testing.T) {
	cs, err := kube.Client(env.MgmtKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cs.CoreV1().Secrets(devenv.WorkloadNamespace).Get(context.Background(), devenv.WorkloadCluster+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(s.Data["value"])
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timeout = 15 * time.Second
	obs("workload Secret server: %s", cfg.Host)
	wcs, err := clientcmdClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wcs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	obs("listing workload nodes through the Cluster's kubeconfig Secret from the host: %v", err)
}
