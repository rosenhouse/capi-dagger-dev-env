package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

func StartRegistry(ctx context.Context, c *dagger.Client) (*Registry, error) {
	svc, err := c.Container().From(registryImage).WithExposedPort(5000).AsService().Start(ctx)
	if err != nil {
		return nil, err
	}
	name, err := svc.Hostname(ctx)
	if err != nil {
		return nil, err
	}
	crane := c.Container().From(craneImage).
		WithServiceBinding("registry", svc).
		WithEnvVariable("DEVENV_SESSION", time.Now().Format(time.RFC3339Nano))
	resolvConf, err := crane.WithExec([]string{"cat", "/etc/resolv.conf"}).Stdout(ctx)
	if err != nil {
		return nil, err
	}
	domain, err := sessionDomain(resolvConf)
	if err != nil {
		return nil, err
	}
	return &Registry{Host: name + "." + domain + ":5000", crane: crane}, nil
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
