# capi-dagger-dev-env

A Cluster API development environment. Each environment runs in its own [smolvm](https://github.com/smol-machines/smolvm) microVM.

## Prerequisites

- Go 1.26
- smolvm 1.22.0:
  `curl -fsSL https://raw.githubusercontent.com/smol-machines/smolvm/v1.22.0/scripts/install.sh | bash -s -- --version 1.22.0`
- On Linux, read and write access to `/dev/kvm`. A cloud VM needs nested virtualization.
- On macOS, Apple Silicon. See [docs/macos.md](docs/macos.md).
- 5 GiB of memory and about 7 GB of disk per environment

## Use

```sh
go run ./cmd/devenv up           # brings up an environment and exits once it is ready
go run ./cmd/devenv test         # brings up an environment, verifies it, and deletes it
go run ./cmd/devenv redeploy     # rebuilds from the current source into a running environment
go run ./cmd/devenv status       # lists environments and the state of their VMs
go run ./cmd/devenv kubeconfig --cluster workload > workload.kubeconfig
go run ./cmd/devenv down --purge # deletes an environment's VM and its state
```

Each environment keeps its kubeconfigs and logs in `.devenv/<name>/`.
Without `--name`, `up` and `test` pick a random name, and `kubeconfig`, `redeploy` and `down` act on the only environment, or else the only running one.
`test` without `--name` deletes the environment's state once it passes.
Every `up` builds a new VM; only downloads are cached, in the user cache directory.
The API servers and the session registry listen on free ports of the host's loopback addresses.
A failure names the stage and the readiness gate that failed, and exports logs to `.devenv/<name>/logs`.
`.devenv/<name>/guest.log` holds the output of commands in the VM, which `--verbose` also streams.
