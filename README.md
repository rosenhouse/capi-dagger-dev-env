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

```sh
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
API server tunnels listen on all host interfaces.
A tunnel stalls while any connection through it stays open with data unread, for example from a suspended `kubectl`. It recovers about 30 seconds after that connection closes.
A failure names the stage and the readiness gate that failed.

## Use with your own controllers

Write a `main` package that passes your commands, packages and tests to `cli.Main`:

```go
func main() {
	cli.Main(devenv.Config{
		Commands: []string{"./cmd/manager"},
		Packages: []devenv.Package{
			{Name: "manager", RefName: "manager.example.com", Config: "config/manager", Images: []string{"manager"}},
		},
		Test: func(ctx context.Context, e *devenv.Environment) error { /* ... */ },
	})
}
```

See [`devenv.Config`](devenv/config.go) for each field, and [`cmd/devenv`](cmd/devenv/main.go) for the Greeting example's configuration.
