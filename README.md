# capi-dagger-dev-env

A Cluster API development environment that runs inside one Dagger session.

## Prerequisites

- Go 1.26
- A container runtime that Dagger can start its engine in, such as Docker or Podman
- cgroup v2 on the engine's host
- `fs.inotify.max_user_instances` of at least 512 and `fs.inotify.max_user_watches` of at least 524288
- About 10 GB of free disk for the Dagger engine

On Apple Silicon macOS, see [docs/macos.md](docs/macos.md).

## Use

[`examples/greeting`](examples/greeting) is a set of example controllers in their own module.
Run its environment from that directory:

```sh
cd examples/greeting
go run ./cmd/devenv up           # holds the environment until Ctrl-C
go run ./cmd/devenv test         # brings up an environment, verifies it, and tears it down
go run ./cmd/devenv redeploy     # rebuilds from the current source into the running environment
go run ./cmd/devenv status       # lists environments and whether each is running
go run ./cmd/devenv kubeconfig --cluster workload > workload.kubeconfig
go run ./cmd/devenv down --purge # stops an environment and deletes its cached Docker data and state
```

Each environment keeps its kubeconfigs and logs in `.devenv/<name>/`.
Without `--name`, `kubeconfig`, `redeploy` and `down` act on the only environment, or else the only running one.
Reusing a name with `--name` reuses that environment's cached images.
`test` without `--name` deletes its environment's data once it passes.
Kubeconfigs reach each API server through a proxy on 127.0.0.1, in front of a Dagger tunnel that listens on all host interfaces.
The proxy closes a connection whose client stops reading, such as a suspended `kubectl`, before it can stall the tunnel.
A failure names the stage and the readiness gate that failed.

## Use with your own controllers

Add the tool to your module with `go get github.com/rosenhouse/capi-dagger-dev-env`.
Then write a `main` package that passes your commands and packages to `cli.Main`:

```go
func main() {
	cli.Main(devenv.Config{
		Commands: []string{"./cmd/manager"},
		Packages: []devenv.Package{
			{Name: "manager", RefName: "manager.example.com", Config: "config/manager", Images: []string{"manager"}},
		},
	})
}
```

Run it from your module, which devenv builds from.
devenv stamps each build's version into a command's `var version string` in package `main`.
See [`devenv.Config`](devenv/config.go) for the hooks, and [`examples/greeting/cmd/devenv`](examples/greeting/cmd/devenv/main.go) for the Greeting example's configuration.
