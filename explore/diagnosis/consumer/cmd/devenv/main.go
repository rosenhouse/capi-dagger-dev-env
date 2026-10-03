// Command devenv runs a consumer whose DIAG_SCENARIO picks packages and hooks that fail in realistic ways.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"dagger.io/dagger"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
)

func pkg(name string, images ...string) devenv.Package {
	return devenv.Package{Name: name, RefName: name + ".diag.example.com", Config: "config/" + name, Images: images}
}

func mark(msg string) { fmt.Fprintf(os.Stderr, "DIAG: %s at %s\n", msg, time.Now().Format(time.RFC3339)) }

func main() {
	cfg := devenv.Config{
		Commands: []string{"./cmd/hello", "./cmd/lister"},
		Packages: []devenv.Package{pkg("good", "hello")},
	}
	switch s := os.Getenv("DIAG_SCENARIO"); s {
	case "good", "":
	case "manifests":
		cfg.Packages = append(cfg.Packages, pkg("crash", "hello"), pkg("typo", "hello"), pkg("nocrd", "hello"),
			pkg("probe", "hello"), pkg("nsmismatch", "hello"))
	case "rbac":
		cfg.Packages = append(cfg.Packages, pkg("rbac", "lister"))
		cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
			mark("test started")
			cs, err := kube.Client(e.MgmtKubeconfig)
			if err != nil {
				return err
			}
			return ready.Wait(ctx, ready.Gate{
				Name: "lister writes its heartbeat", Timeout: 90 * time.Second, Interval: 5 * time.Second,
				Check: func(ctx context.Context) error {
					_, err := cs.CoreV1().ConfigMaps("rbac").Get(ctx, "lister-heartbeat", metav1.GetOptions{})
					return err
				},
			})
		}
	case "web":
		cfg.Images = func(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
			return map[string]*dagger.Container{"web": src.Directory("web").DockerBuild()}
		}
		cfg.Packages = append(cfg.Packages, pkg("web", "web"))
	case "ready-fail":
		cfg.Ready = func(ctx context.Context, e *devenv.Environment) error {
			mark("ready started")
			return ready.Wait(ctx, ready.Gate{
				Name: "widget controller Ready", Timeout: 90 * time.Second, Interval: 5 * time.Second,
				Check: func(context.Context) error {
					return errors.New(`widget "demo" has Ready=False: webhook not serving`)
				},
			})
		}
	case "ready-hang":
		cfg.Ready = func(ctx context.Context, e *devenv.Environment) error {
			mark("ready started")
			return ready.Wait(ctx, ready.Gate{
				Name: "widget controller Ready", Timeout: 30 * time.Minute, Interval: 5 * time.Second,
				Check: func(context.Context) error {
					return errors.New(`widget "demo" has Ready=False: webhook not serving`)
				},
			})
		}
	case "test-fail":
		cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
			mark("test started")
			return errors.New(`e2e: workload cluster served "" for greeting "demo", want "hi"`)
		}
	case "test-panic":
		cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
			mark("test started")
			var replicas map[string]*int32
			fmt.Println(*replicas["hello"])
			return nil
		}
	case "test-hang":
		cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
			mark("test started")
			<-ctx.Done()
			return fmt.Errorf("e2e: waiting for greeting: %w", ctx.Err())
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown DIAG_SCENARIO %q\n", s)
		os.Exit(2)
	}
	// DIAG_DEADLINE ends the run after that many seconds, the way a wrapping test's deadline would.
	secs, _ := strconv.Atoi(os.Getenv("DIAG_DEADLINE"))
	if secs == 0 {
		cli.Main(cfg)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()
	if err := cli.Command(cfg).ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
