// Command devenv runs Acme's addon packages in a Cluster API development environment.
// DEVENV_SCENARIO picks a variant of the configuration for exploration.
package main

import (
	"os"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/cli"
)

var (
	addonManager = devenv.Package{Name: "addon-manager", RefName: "addon-manager.acme.example.com",
		Config: "packages/addon-manager/bundle/config", Images: []string{"addon-manager"}}
	agent = devenv.Package{Name: "agent", RefName: "agent.acme.example.com",
		Config: "packages/agent/bundle/config", Images: []string{"agent"}, On: devenv.Workload}
)

func main() {
	cfg := devenv.Config{
		Commands: []string{"./cmd/addon-manager", "./cmd/agent"},
		Packages: []devenv.Package{addonManager, agent},
	}
	switch os.Getenv("DEVENV_SCENARIO") {
	case "failures":
		// Management packages that a Carvel team might already have.
		cfg.Packages = append(cfg.Packages,
			devenv.Package{Name: "dns-config", RefName: "dns-config.acme.example.com", Config: "packages/dns-config/config"},
			devenv.Package{Name: "legacy", RefName: "legacy.acme.example.com", Config: "packages/legacy/bundle", Images: []string{"agent"}},
			devenv.Package{Name: "plain-values", RefName: "plain-values.acme.example.com", Config: "packages/plain-values/config"},
		)
	case "workaround":
		cfg.Packages = append(cfg.Packages,
			devenv.Package{Name: "dns-config", RefName: "dns-config.acme.example.com", Config: "packages/dns-config/config"})
	case "locks":
		// A bundle root with only an images lock, a committed kbld lock, and a Helm chart.
		cfg.Packages = append(cfg.Packages,
			devenv.Package{Name: "legacy-lock", RefName: "legacy-lock.acme.example.com", Config: "packages/legacy-lock/bundle", Images: []string{"agent"}},
			devenv.Package{Name: "kbld-locked", RefName: "kbld-locked.acme.example.com", Config: "packages/kbld-locked/config", Images: []string{"agent"}},
			devenv.Package{Name: "helm-chart", RefName: "helm-chart.acme.example.com", Config: "packages/helm-chart/chart", Images: []string{"agent"}},
		)
	case "collide":
		cfg.Packages = append(cfg.Packages,
			devenv.Package{Name: "metrics", RefName: "metrics.acme.example.com", Config: "packages/metrics/config"})
	case "badref":
		cfg.Packages[0].RefName = "addon-manager"
	case "badname":
		cfg.Packages[0].Name = "Addon_Manager"
	case "prodrefs":
		// The team's config keeps its production image reference; an Images hook builds it under that name.
		cfg.Commands = []string{"./cmd/agent"}
		cfg.Packages[0].Images = []string{prodRef}
		cfg.Images = func(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container {
			bin := c.Container().From("golang:1.26.1").
				WithMountedCache("/go/pkg/mod", c.CacheVolume("acme-go-mod")).
				WithEnvVariable("CGO_ENABLED", "0").
				WithDirectory("/src", src).WithWorkdir("/src").
				WithExec([]string{"go", "build", "-ldflags", "-X main.version=" + version, "-o", "/out/addon-manager", "./cmd/addon-manager"}).
				File("/out/addon-manager")
			return map[string]*dagger.Container{prodRef: c.Container().From("gcr.io/distroless/static:nonroot").
				WithFile("/addon-manager", bin).WithEntrypoint([]string{"/addon-manager"})}
		}
		cfg.Packages = append(cfg.Packages,
			devenv.Package{Name: "third-party", RefName: "third-party.acme.example.com", Config: "packages/third-party/config"})
	}
	cli.Main(cfg)
}

const prodRef = "ghcr.io/acme/addon-manager:v1.4.0"
