# capi-dagger-dev-env

A Cluster API development environment. Each environment runs in its own [smolvm](https://github.com/smol-machines/smolvm) microVM.

## Prerequisites

- Go 1.26
- smolvm 1.22.0:
  `curl -fsSL https://raw.githubusercontent.com/smol-machines/smolvm/v1.22.0/scripts/install.sh | bash -s -- --version 1.22.0`
- On Linux, read and write access to `/dev/kvm`. A cloud VM needs nested virtualization.
- On macOS, Apple Silicon. See [docs/macos.md](docs/macos.md).
- Per environment: 5 GiB of memory, about 7 GB of disk, and about half a CPU core while idle
- Direct access to the internet. The VM resolves names with 1.1.1.1 and does not use the host's HTTP proxy.

## Use

```sh
go run ./cmd/devenv up           # brings up an environment and exits once it is ready
go run ./cmd/devenv test         # brings up an environment, verifies it, and deletes it
go run ./cmd/devenv redeploy     # rebuilds from the current source into a running environment
go run ./cmd/devenv status       # lists environments, the state of their VMs, and their host ports
go run ./cmd/devenv kubeconfig --cluster workload > workload.kubeconfig
go run ./cmd/devenv down --purge # deletes an environment's VM and its state
```

Each environment keeps its kubeconfigs and logs in `.devenv/<name>/` at the root of this repository.
Without `--name`, `up` acts on the only environment, or else creates one with a random name.
`test` without `--name` picks a random name, and deletes the environment's state once it passes.
`kubeconfig`, `redeploy` and `down` act on the only environment, or else the only running one.

Every `up` builds a new VM. Downloads, the distroless base image and Go's build cache persist in the user cache directory.
Images do not: each VM pulls the platform's images from their registries. A pull-through mirror (R8) comes later.

The API servers and the environment's registry listen on free ports of the host's loopback addresses, which `status` shows.
A failure names the stage and the readiness gate that failed, and exports logs to `.devenv/<name>/logs`.
`--retain` keeps the VM of a failed `up` or `test` for debugging.
`.devenv/<name>/guest.log` holds the output of commands in the VM and each check of a readiness gate. `--verbose` also streams it.
