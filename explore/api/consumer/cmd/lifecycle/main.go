// Command lifecycle drives devenv.Up directly, as a Go program or test harness would.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"

	"example.com/legacyctl/internal/scenario"
)

func obs(format string, args ...any) { fmt.Printf("OBS: "+format+"\n", args...) }

func main() {
	mode, name, stateDir := os.Args[1], os.Args[2], os.Args[3]
	cfg := scenario.Config("default")
	cfg.Test = nil
	opts := devenv.Options{Name: name, StateDir: stateDir, Progress: os.Stderr}
	start := time.Now()
	switch mode {
	case "deadline":
		// The caller bounds bring-up, as a test with a deadline would.
		d, _ := time.ParseDuration(os.Args[4])
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		e, err := devenv.Up(ctx, cfg, opts)
		obs("deadline %v: Up returned after %v, err=%v", d, time.Since(start).Round(time.Second), err)
		if e != nil {
			e.Close()
		}
	case "bounded":
		// The caller bounds only bring-up, then uses the environment.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		e, err := devenv.Up(ctx, cfg, opts)
		obs("bounded: Up returned after %v, err=%v", time.Since(start).Round(time.Second), err)
		if err != nil {
			cancel()
			os.Exit(1)
		}
		obs("bounded: nodes before cancel: %v", listNodes(e.MgmtKubeconfig))
		cancel()
		time.Sleep(15 * time.Second)
		obs("bounded: env ctx after cancel: %v", e.Context().Err())
		obs("bounded: nodes 15s after cancelling Up's ctx: %v", listNodes(e.MgmtKubeconfig))
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Minute)
		obs("bounded: Redeploy after cancel: %v", e.Redeploy(rctx, "after-cancel", os.Stderr))
		rcancel()
		obs("bounded: Close: %v", e.Close())
	default:
		panic("unknown mode " + mode)
	}
}

func listNodes(kubeconfig string) error {
	cs, err := kube.Client(kubeconfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes")
	}
	return nil
}
