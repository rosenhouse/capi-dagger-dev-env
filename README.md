# capi-dagger-dev-env

A Cluster API development environment that runs inside one Dagger session.

## Prerequisites

- Go 1.26
- A container runtime that Dagger can start its engine in, such as Docker or Podman
- cgroup v2 on the engine's host
- `fs.inotify.max_user_instances` of at least 512 and `fs.inotify.max_user_watches` of at least 524288

## Use

```sh
go run ./cmd/devenv up     # holds the environment until Ctrl-C
go run ./cmd/devenv test   # brings up an environment, verifies it, and tears it down
```

`up` prints the path of the management cluster's kubeconfig.
Its API server tunnel listens on all host interfaces.

Each environment keeps its kubeconfigs and logs in `.devenv/<name>/`.
A failure names the stage and the readiness gate that failed.
