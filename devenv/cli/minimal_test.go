package cli_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
)

// minimal configures testdata/minimal, a consumer with one command and one package.
var minimal = devenv.Config{
	Commands: []string{"./cmd/hello"},
	Packages: []devenv.Package{{Name: "hello", RefName: "hello.minimal.example.com", Config: "config/hello", Images: []string{"hello"}}},
}

func TestMinimalConsumerConfigNamesItsImages(t *testing.T) {
	for _, p := range minimal.Packages {
		placeholders, err := bundle.Placeholders(filepath.Join("testdata/minimal", p.Config))
		if err != nil || !slices.Equal(placeholders, p.Images) {
			t.Errorf("package %s: placeholders %v, %v; images %v", p.Name, placeholders, err, p.Images)
		}
	}
}

// TestMinimalConsumer runs `devenv test` for the minimal consumer from its module, as an adopter would.
// Its Test redeploys and checks that hello's image changed.
// It needs a Dagger engine that can run Kind, so it runs only with DEVENV_E2E set.
func TestMinimalConsumer(t *testing.T) {
	if os.Getenv("DEVENV_E2E") == "" {
		t.Skip("set DEVENV_E2E to bring up an environment")
	}
	t.Chdir("testdata/minimal")
	cfg := minimal
	ready := false
	cfg.Ready = func(context.Context, *devenv.Environment) error { ready = true; return nil }
	cfg.Test = func(ctx context.Context, e *devenv.Environment) error {
		before, err := helloImage(ctx, e)
		if err != nil {
			return err
		}
		if err := e.Redeploy(ctx, "v2", io.Discard); err != nil {
			return err
		}
		after, err := helloImage(ctx, e)
		if err != nil {
			return err
		}
		if after == before {
			return errors.New("redeploy left hello's image at " + before)
		}
		return nil
	}
	// Ending before the test's deadline lets devenv export logs and name the gate that hung.
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-2*time.Minute))
		defer cancel()
	}
	cmd := cli.Command(cfg)
	cmd.SetArgs([]string{"test"})

	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("devenv test did not call Ready")
	}
}

func helloImage(ctx context.Context, e *devenv.Environment) (string, error) {
	cs, err := kube.Client(e.MgmtKubeconfig)
	if err != nil {
		return "", err
	}
	d, err := cs.AppsV1().Deployments("hello").Get(ctx, "hello", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return d.Spec.Template.Spec.Containers[0].Image, nil
}
