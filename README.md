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
Images do not: a cold start pulls the platform's images from their registries. A pull-through mirror (R8) comes later.

The API servers and the environment's registry listen on free ports of the host's loopback addresses, which `status` shows.
A failure names the stage and the readiness gate that failed, and exports logs to `.devenv/<name>/logs`.
`--retain` keeps the VM of a failed `up` or `test` for debugging.
`.devenv/<name>/guest.log` holds the output of commands in the VM and each check of a readiness gate. `--verbose` also streams it.

## Warm starts

`up` and `test` restore the platform from this host's checkpoint if there is one, and else bring it up cold.
The platform is the VM with dockerd, the registry, both clusters, kapp-controller, CAPI and CAPD, but no first-party code.
`--cold` ignores the checkpoint. A restore that fails falls back to a cold start, unless `--warm` or `--retain` is set.

```sh
go run ./cmd/devenv platform save   # brings up the platform in a new VM and saves it as this host's checkpoint
go run ./cmd/devenv platform key    # prints the checkpoint's key; --inputs prints what it hashes
rm -r ~/.cache/devenv/platform      # removes the checkpoint, on Linux
smolvm pack prune --all             # removes smolvm's unused copies of checkpoints, and its other unused caches
```

The key hashes the code that brings up and captures the platform, and the host's OS, architecture and CPU type.
A change to that code needs a new `platform save`, which also removes the host's other checkpoints.
A checkpoint restores only on a CPU of the same type. `up` starts cold instead of restoring one captured over 35 days ago.

A checkpoint takes about 3 GB in `platform/` under the user cache directory.
smolvm extracts about 9.5 GB from it on its first restore, under `~/.cache/smolvm`, and keeps that after `down`.

Environments restored from one checkpoint share their clusters' CAs, service account keys,
and the UIDs of the `kube-system` namespace and of the workload Cluster.

CI publishes checkpoints only from pushes to `smol`, and only for AMD EPYC 7763 runners.
To evict a broken one, run `gh cache delete <key>`, with the key from a job's summary.

Warm starts are tested only on x86_64 Linux. On macOS, `platform save` does not zero-fill guest memory, so its checkpoints are larger.
