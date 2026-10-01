# Apple Silicon macOS

On Apple Silicon, smolvm runs each environment's VM with Hypervisor.framework.
The VM has the host's architecture, so images build for arm64.
CI covers only x86_64 Linux.

smolvm's macOS binary is ad-hoc signed and not notarized.
If a browser downloaded it, clear the quarantine with `xattr -dr com.apple.quarantine` on its directory.

## Checklist

To validate devenv on smolvm, run these from a clean checkout and report the results on #32.

1. `time go run ./cmd/devenv test --name mac` exits 0.
2. Run it again and note both times; the second should skip the downloads.
3. `go run ./cmd/devenv up --name mac`. Once it prints `Environment mac is up.`:
   1. `go run ./cmd/devenv status` shows `mac` running.
   2. `go run ./cmd/devenv kubeconfig --cluster workload > /tmp/workload.kubeconfig`, then `export KUBECONFIG=/tmp/workload.kubeconfig`.
   3. `kubectl get nodes` lists Ready nodes.
   4. `kubectl -n kube-system logs deploy/coredns` prints logs.
   5. `kubectl -n kube-system exec $(kubectl -n kube-system get pod -l component=etcd -o name | head -1) -- etcdctl version` prints a version.
   6. `kubectl -n kube-system port-forward deploy/coredns 8080`, then `curl localhost:8080/health` in another terminal prints `OK`.
   7. Edit the reply format in `cmd/hello/main.go`, then `go run ./cmd/devenv redeploy --name mac` prints `Redeployed environment mac.`
   8. `go run ./cmd/devenv down --name mac --purge` deletes the VM and removes `.devenv/mac`.
4. Report smolvm's version, the macOS version, the chip, and the times from steps 1 and 2.
