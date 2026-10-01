package devenv

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
)

// minimal is a consumer with one command and one package, and no Greeting example.
func minimal(t *testing.T) Config {
	t.Helper()
	root, err := filepath.Abs("testdata/minimal")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Root:     root,
		Commands: []string{"./cmd/hello"},
		Packages: []Package{{Name: "hello", RefName: "hello.minimal.example.com", Config: "config/hello", Images: []string{"hello"}}},
	}
}

func TestMinimalConsumerIsValid(t *testing.T) {
	c := minimal(t)
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.checkPaths(); err != nil {
		t.Fatal(err)
	}
	placeholders, err := bundle.Placeholders(filepath.Join(c.Root, c.Packages[0].Config))
	if err != nil || len(placeholders) != 1 || placeholders[0] != "hello" {
		t.Errorf("placeholders = %v, %v", placeholders, err)
	}
}

// TestMinimalConsumer brings up an environment for the minimal consumer, then redeploys it.
// It needs a Dagger engine that can run Kind, so it runs only with DEVENV_E2E set.
func TestMinimalConsumer(t *testing.T) {
	if os.Getenv("DEVENV_E2E") == "" {
		t.Skip("set DEVENV_E2E to bring up an environment")
	}
	c := minimal(t)
	ready := false
	c.Ready = func(context.Context, *Environment) error { ready = true; return nil }
	ctx := context.Background()

	e, err := Up(ctx, c, Options{Name: "minimal-" + strings.ToLower(rand.Text()[:6]), StateDir: ".devenv", Progress: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
		if err := Purge(context.WithoutCancel(ctx), e.Env); err != nil {
			t.Error(err)
		}
	})

	if !ready {
		t.Error("Up did not call Ready")
	}
	if err := e.Verify(ctx); err != nil {
		t.Error(err)
	}
	if err := e.Redeploy(ctx, "v2", io.Discard); err != nil {
		t.Error(err)
	}
}
