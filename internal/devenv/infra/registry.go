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

// Registry is the session's plain-HTTP OCI registry.
type Registry struct {
	// Host is the registry's FQDN and port. Nested containers resolve it through DNS,
	// and its .local suffix makes go-containerregistry clients use plain HTTP.
	Host  string
	crane *dagger.Container
}

// StartRegistry starts the session registry that holds first-party images and bundles.
func StartRegistry(ctx context.Context, c *dagger.Client) (*Registry, error) {
	host, err := startRegistryService(ctx, c, c.Container().From(registryImage))
	if err != nil {
		return nil, err
	}
	return &Registry{Host: host, crane: c.Container().From(craneImage).With(InSession)}, nil
}

// Upstreams maps each registry that nodes pull from to its URL.
var Upstreams = map[string]string{
	"docker.io":       "https://registry-1.docker.io",
	"registry.k8s.io": "https://registry.k8s.io",
	"ghcr.io":         "https://ghcr.io",
	"quay.io":         "https://quay.io",
}

// Mirrors maps each upstream in Upstreams to the host:port of its pull-through mirror.
type Mirrors map[string]string

// StartMirrors starts a pull-through mirror for each upstream. Mirror storage persists across sessions and environments.
func StartMirrors(ctx context.Context, c *dagger.Client) (Mirrors, error) {
	mirrors := Mirrors{}
	for name, url := range Upstreams {
		host, err := startRegistryService(ctx, c, c.Container().From(registryImage).
			WithEnvVariable("REGISTRY_PROXY_REMOTEURL", url).
			WithMountedCache("/var/lib/registry", c.CacheVolume("devenv-mirror-"+name),
				dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeShared}))
		if err != nil {
			return nil, fmt.Errorf("mirror %s: %w", name, err)
		}
		mirrors[name] = host
	}
	return mirrors, nil
}

// startRegistryService starts ctr as a registry on port 5000 and returns its FQDN and port.
func startRegistryService(ctx context.Context, c *dagger.Client, ctr *dagger.Container) (string, error) {
	svc, err := ctr.WithExposedPort(5000).AsService().Start(ctx)
	if err != nil {
		return "", err
	}
	name, err := svc.Hostname(ctx)
	if err != nil {
		return "", err
	}
	resolvConf, err := c.Container().From(craneImage).
		With(InSession).
		WithExec([]string{"cat", "/etc/resolv.conf"}).
		Stdout(ctx)
	if err != nil {
		return "", err
	}
	domain, err := sessionDomain(resolvConf)
	if err != nil {
		return "", err
	}
	return name + "." + domain + ":5000", nil
}

// Catalog lists the repositories in the registry at host.
func (r *Registry) Catalog(ctx context.Context, host string) (string, error) {
	return r.crane.
		WithExec([]string{"crane", "catalog", "--insecure", host}).
		Stdout(ctx)
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
