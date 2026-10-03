// Package scenario configures the legacyctl consumer for one exploratory scenario.
package scenario

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"dagger.io/dagger"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
)

// ReadyCalls counts calls of the Ready hook.
var ReadyCalls atomic.Int32

func obs(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "OBS-HOOK: "+format+"\n", args...)
}

func deadline(ctx context.Context) string {
	if d, ok := ctx.Deadline(); ok {
		return "deadline in " + time.Until(d).Round(time.Second).String()
	}
	return "no deadline"
}

var manager = devenv.Package{Name: "manager", RefName: "manager.legacyctl.example.com", Config: "config/manager", Images: []string{"manager"}}
var extras = devenv.Package{Name: "extras", RefName: "extras.legacyctl.example.com", Config: "config/extras"}
var addon = devenv.Package{Name: "addon", RefName: "addon.legacyctl.example.com", Config: "config/addon", On: devenv.Workload}

func ready(ctx context.Context, e *devenv.Environment) error {
	n := ReadyCalls.Add(1)
	obs("Ready call %d, ctx %s, env ctx %s", n, deadline(ctx), deadline(e.Context()))
	return nil
}

func test(ctx context.Context, e *devenv.Environment) error {
	obs("Test called, ctx %s, Ready calls so far %d", deadline(ctx), ReadyCalls.Load())
	cs, err := kube.Client(e.WorkloadKubeconfig)
	if err != nil {
		return err
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	obs("Test sees %d workload nodes", len(nodes.Items))
	return nil
}

// Config returns the consumer's Config for the named scenario.
func Config(name string) devenv.Config {
	cfg := devenv.Config{
		Commands: []string{"./cmd/manager"},
		Packages: []devenv.Package{manager, extras, addon},
		Images: func(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
			obs("Images hook called with version %s", version)
			return nil
		},
		Ready: ready,
		Test:  test,
	}
	switch name {
	case "", "default":
	case "ready-error":
		cfg.Ready = func(context.Context, *devenv.Environment) error { return errors.New("ready-boom") }
	case "test-panic":
		cfg.Test = func(context.Context, *devenv.Environment) error { panic("test-boom") }
	case "test-hang":
		cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
			obs("Test started, ctx %s; hanging until ctx is done", deadline(ctx))
			<-ctx.Done()
			obs("Test ctx done: %v", context.Cause(ctx))
			return ctx.Err()
		}
	case "workload-only":
		cfg.Packages = []devenv.Package{{Name: "manager", RefName: "manager.legacyctl.example.com", Config: "config/manager", Images: []string{"manager"}, On: devenv.Workload}}
		cfg.Ready, cfg.Test = nil, nil
	case "images-nil":
		cfg.Images = func(*dagger.Client, *dagger.Directory, string) map[string]*dagger.Container {
			return map[string]*dagger.Container{"extra": nil}
		}
		cfg.Packages = append(cfg.Packages, devenv.Package{Name: "extra", RefName: "extra.legacyctl.example.com", Config: "config/extras", Images: []string{"extra"}})
	case "sibling":
		cfg.Commands = append(cfg.Commands, "./cmd/usesapi")
	case "test-fail":
		cfg.Test = func(context.Context, *devenv.Environment) error { return errors.New("test-fail") }
	case "name-upper":
		cfg.Packages = []devenv.Package{{Name: "Manager", RefName: manager.RefName, Config: manager.Config, Images: manager.Images}}
	case "refname-short":
		cfg.Packages = []devenv.Package{{Name: "manager", RefName: "manager", Config: manager.Config, Images: manager.Images}}
	case "dup-refname":
		cfg.Packages = []devenv.Package{manager, {Name: "extras", RefName: manager.RefName, Config: "config/extras"}}
	case "bad-config":
		cfg.Packages = []devenv.Package{manager, {Name: "broken", RefName: "broken.legacyctl.example.com", Config: "config/broken"}}
	case "root-cmd":
		cfg.Commands = []string{"./"}
		cfg.Packages = []devenv.Package{{Name: "manager", RefName: manager.RefName, Config: manager.Config, Images: []string{"."}}}
	case "non-main":
		cfg.Commands = append(cfg.Commands, "./internal/scenario")
	case "hook-dockerfile-missing":
		cfg.Images = func(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
			return map[string]*dagger.Container{"sidecar": src.DockerBuild(dagger.DirectoryDockerBuildOpts{Dockerfile: "build/sidecar/Dockerfile"})}
		}
		cfg.Packages = []devenv.Package{manager, {Name: "sidecar", RefName: "sidecar.legacyctl.example.com", Config: "config/extras", Images: []string{"sidecar"}}}
	case "image-missing":
		cfg.Packages = []devenv.Package{{Name: "manager", RefName: "manager.legacyctl.example.com", Config: "config/manager", Images: []string{"manager", "nope"}}}
	default:
		panic("unknown scenario " + name)
	}
	return cfg
}
