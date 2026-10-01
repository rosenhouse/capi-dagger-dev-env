# Apple Silicon macOS

devenv runs everything inside the Dagger engine, a Linux container in your container runtime's VM.
Images build for the engine's platform, so on Apple Silicon they are arm64.
CI covers arm64 Linux; the runtimes below are not tested in CI.

## Choose a runtime

Dagger looks for a `docker`, `container`, `podman`, `finch` or `nerdctl` CLI on `PATH`, in that order, and uses the first it finds.
If that runtime is not running, Dagger fails rather than trying the next one.
For example, a Homebrew `docker` CLI makes Dagger ignore Podman while Docker Desktop is quit.

To pick a runtime explicitly, set `_EXPERIMENTAL_DAGGER_RUNNER_HOST`, for example
`image+podman://registry.dagger.io/engine:v0.21.10`.
The engine version must match `dagger.io/dagger` in `go.mod`.

Size the VM for the README's prerequisites. CI runs on 4 CPUs and 16 GB of memory; smaller VMs are untested.
Check the VM's inotify limits with your runtime's CLI:

```sh
docker run --rm alpine sysctl fs.inotify
```

### Docker Desktop or OrbStack

Raise the VM's CPU, memory and disk limits in the app's settings.

### Podman

Use a rootful machine. Rootless Podman is untested.

```sh
podman machine init --cpus 4 --memory 16384 --rootful
podman machine start
podman machine ssh "printf 'fs.inotify.max_user_instances=512\nfs.inotify.max_user_watches=524288\n' | sudo tee /etc/sysctl.d/99-devenv.conf && sudo sysctl --system"
```

### Apple `container` (experimental)

Each container runs in its own lightweight VM, and there is no `--privileged` flag; Dagger passes `--cap-add ALL` instead.

```sh
container system start
```

## Checklist

Run these from a clean checkout and report the results on #13.

1. `time go run ./cmd/devenv test --name mac` exits 0.
2. Run it again and note both times; the second should be faster.
3. `go run ./cmd/devenv up --name mac`. Once it prints `Environment mac is up.`, in another terminal:
   1. `go run ./cmd/devenv status` shows `mac` running.
   2. `go run ./cmd/devenv kubeconfig --cluster workload > /tmp/workload.kubeconfig`, then `export KUBECONFIG=/tmp/workload.kubeconfig`.
   3. `kubectl get nodes` lists Ready nodes.
   4. `kubectl -n kube-system logs deploy/coredns` prints logs.
   5. `kubectl -n kube-system exec $(kubectl -n kube-system get pod -l component=etcd -o name | head -1) -- etcdctl version` prints a version.
   6. `kubectl -n kube-system port-forward deploy/coredns 8080`, then `curl localhost:8080/health` in a third terminal prints `OK`.
   7. Edit the reply format in `cmd/hello/main.go`, then `go run ./cmd/devenv redeploy --name mac` prints `Redeployed environment mac.`
   8. `go run ./cmd/devenv down --name mac --purge` stops `up` and removes `.devenv/mac`.
4. Report the runtime and its version, the macOS version, the chip, and the times from steps 1 and 2.
