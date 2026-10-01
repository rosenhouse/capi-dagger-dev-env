package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dagger.io/dagger"
)

const (
	registryImage = "registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
	craneImage    = "gcr.io/go-containerregistry/crane:debug@sha256:e78770b31258a3846f878036d9c1f63fbe4c871f9f56990bf77fd95c013e3c1b"
)

// Registry is a plain-HTTP OCI registry service in the session.
type Registry struct {
	// Host is the registry's FQDN and port. Nested containers resolve it through DNS,
	// and its .local suffix makes go-containerregistry clients use plain HTTP.
	Host  string
	crane *dagger.Container
}

// Upstreams maps each registry that nodes pull from to its URL.
var Upstreams = map[string]string{
	"docker.io":       "https://registry-1.docker.io",
	"registry.k8s.io": "https://registry.k8s.io",
	"ghcr.io":         "https://ghcr.io",
	"quay.io":         "https://quay.io",
	"gcr.io":          "https://gcr.io",
}

// Mirrors maps each upstream in Upstreams to its pull-through mirror.
type Mirrors map[string]*Registry

// StartRegistries starts the session registry, which holds first-party images and bundles,
// and a pull-through mirror for each upstream.
//
// Mirror storage persists across sessions, and environments running at once share it. Concurrent
// registry processes write blobs safely, but can race on tag links; a reader that hits one falls back to upstream.
// They also rewrite one scheduler-state.json every few seconds, so a mirror starting mid-write can fail to start,
// and expiry entries can be lost.
func StartRegistries(ctx context.Context, c *dagger.Client) (*Registry, Mirrors, error) {
	resolvConf, err := c.Container().From(craneImage).With(InSession).WithExec([]string{"cat", "/etc/resolv.conf"}).Stdout(ctx)
	if err != nil {
		return nil, nil, err
	}
	domain, err := sessionDomain(resolvConf)
	if err != nil {
		return nil, nil, err
	}
	start := func(ctr *dagger.Container) (*Registry, error) {
		svc, err := ctr.WithExposedPort(5000).AsService().Start(ctx)
		if err != nil {
			return nil, err
		}
		name, err := svc.Hostname(ctx)
		if err != nil {
			return nil, err
		}
		return &Registry{Host: name + "." + domain + ":5000", crane: c.Container().From(craneImage).With(InSession)}, nil
	}

	session, err := start(c.Container().From(registryImage))
	if err != nil {
		return nil, nil, err
	}
	mirrors := Mirrors{}
	for name, url := range Upstreams {
		mirrors[name], err = start(c.Container().From(registryImage).
			WithEnvVariable("REGISTRY_PROXY_REMOTEURL", url).
			WithMountedCache("/var/lib/registry", c.CacheVolume("devenv-mirror-"+name),
				dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeShared}))
		if err != nil {
			return nil, nil, fmt.Errorf("mirror %s: %w", name, err)
		}
	}
	return session, mirrors, nil
}

// Catalog lists the registry's repositories.
func (r *Registry) Catalog(ctx context.Context) (string, error) {
	return r.crane.WithExec([]string{"crane", "catalog", "--insecure", r.Host}).Stdout(ctx)
}

// hostsTOML renders containerd registry config that tries mirror, then falls back to server.
func hostsTOML(server, mirror string) string {
	return fmt.Sprintf("server = %q\n\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n", server, "http://"+mirror)
}

// Push pushes ctr to repo and returns its digest reference.
func (r *Registry) Push(ctx context.Context, ctr *dagger.Container, repo string) (string, error) {
	out, err := r.crane.
		WithMountedFile("/image.tar", ctr.AsTarball()).
		WithExec([]string{"crane", "push", "--insecure", "/image.tar", r.Host + "/" + repo}).
		Stdout(ctx)
	if execErr := (*dagger.ExecError)(nil); errors.As(err, &execErr) {
		return "", fmt.Errorf("%w\n%s", err, tail(execErr.Stderr, 20))
	}
	return strings.TrimSpace(out), err
}

// sessionDomain finds Dagger's session domain among resolv.conf search domains, which may include the host's.
func sessionDomain(resolvConf string) (string, error) {
	for _, line := range strings.Split(resolvConf, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "search" {
			continue
		}
		for _, domain := range fields[1:] {
			if strings.HasSuffix(domain, ".dagger.local") {
				return domain, nil
			}
		}
	}
	return "", errors.New("no *.dagger.local search domain in resolv.conf")
}
