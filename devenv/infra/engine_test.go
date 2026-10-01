package infra_test

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"testing"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/infra"
)

// TestPurgeEmptiesTheDockerDataVolume needs a Dagger engine, so it runs only with DEVENV_ENGINE_TESTS set.
func TestPurgeEmptiesTheDockerDataVolume(t *testing.T) {
	if os.Getenv("DEVENV_ENGINE_TESTS") == "" {
		t.Skip("set DEVENV_ENGINE_TESTS to run against a Dagger engine")
	}
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	envID := "purge-test-" + rand.Text()[:8]
	volume := c.CacheVolume("devenv-" + envID + "-docker")
	// Each listing gets its own label, so the engine cannot reuse an earlier one.
	listing := func(label string) string {
		out, err := c.Container().From("alpine:3").
			With(infra.InSession).
			WithEnvVariable("LISTING", label).
			WithMountedCache("/var/lib/docker", volume, dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
			WithExec([]string{"ls", "-A", "/var/lib/docker"}).
			Stdout(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := c.Container().From("alpine:3").
		With(infra.InSession).
		WithMountedCache("/var/lib/docker", volume, dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithExec([]string{"sh", "-c", "mkdir /var/lib/docker/overlay2 && touch /var/lib/docker/.hidden"}).
		Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if listing("before") == "" {
		t.Fatal("setup left the volume empty")
	}

	if err := infra.Purge(ctx, c, envID); err != nil {
		t.Fatal(err)
	}

	if out := listing("after"); out != "" {
		t.Errorf("volume after Purge holds %q", out)
	}
}
