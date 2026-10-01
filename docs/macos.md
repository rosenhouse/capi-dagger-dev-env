# Apple Silicon macOS

devenv runs everything inside the Dagger engine, a Linux container in your container runtime's VM.
Images build for the engine's platform, so on Apple Silicon they are arm64.
CI covers arm64 Linux; the runtimes below are not tested in CI.

## Choose a runtime

Dagger uses the first runtime it finds: Docker, then Apple `container`, then Podman.
To pick one explicitly, set `_EXPERIMENTAL_DAGGER_RUNNER_HOST`, for example
`image+podman://registry.dagger.io/engine:v0.21.10`.

Every runtime's VM needs:

- 4 CPUs and 8 GB of memory
- 30 GB of disk
- cgroup v2
- `fs.inotify.max_user_instances` of at least 512 and `fs.inotify.max_user_watches` of at least 524288

### Docker Desktop or OrbStack

Raise the VM's CPU, memory and disk limits in the app's settings.

### Podman

Use a rootful machine; rootless Podman cannot run the nested containers that Kind needs.

```sh
podman machine init --cpus 4 --memory 8192 --disk-size 30 --rootful
podman machine start
podman machine ssh sudo sysctl -w fs.inotify.max_user_instances=512 fs.inotify.max_user_watches=524288
```

### Apple `container` (experimental)

Each container runs in its own lightweight VM, and there is no `--privileged` flag; Dagger passes `--cap-add ALL` instead.
The VM's kernel must support the bridge and netfilter features that Docker-in-Docker and kube-proxy need.

```sh
container system start
```

## Checklist

Run these from a clean checkout and report the results on #13.

1. `go run ./cmd/devenv test` exits 0.
2. Run it again and note both times; the second should be faster.
3. `go run ./cmd/devenv up --name mac`, then in another terminal:
   1. `go run ./cmd/devenv status` shows `mac` running.
   2. `kubectl --kubeconfig .devenv/mac/mgmt.kubeconfig get nodes` and the same with `workload.kubeconfig` list Ready nodes.
   3. On the workload cluster, `kubectl logs`, `kubectl exec` and `kubectl port-forward` work against a pod in `greeting-controller`.
   4. `go run ./cmd/devenv down` stops `up`.
4. Report the runtime and its version, the macOS version, the chip, and the times from step 2.
