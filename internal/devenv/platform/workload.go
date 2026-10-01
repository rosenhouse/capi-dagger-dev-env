package platform

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/infra"
)

const (
	podCIDR = "192.168.0.0/16"
	// nodeCertsDir is where containerd in a CAPD node reads registry config.
	nodeCertsDir = "/etc/containerd/certs.d"
)

//go:embed kindnet.yaml
var kindnet string

// CreateWorkloadCluster applies CAPD's quick-start ClusterClass, a kindnet ClusterResourceSet, and one Cluster
// with a control plane node and a worker node. The nodes read the containerd registry config the Kind node reads.
func CreateWorkloadCluster(ctx context.Context, vm *infra.VM, name, namespace string) error {
	clusterClass, err := vm.Output(ctx, "cat "+providerPath("infrastructure-docker", "clusterclass-quick-start.yaml"))
	if err != nil {
		return err
	}
	patched, err := patchClusterClass([]byte(clusterClass), namespace, infra.ContainerdCertsDir, nodeCertsDir)
	if err != nil {
		return err
	}
	crs, err := cniResourceSet(namespace, podCIDR)
	if err != nil {
		return err
	}
	if err := Apply(ctx, vm, append(append(patched, "\n---\n"...), crs...)); err != nil {
		return err
	}
	return vm.Run(ctx, workloadClusterScript(name, namespace))
}

func workloadClusterScript(name, namespace string) string {
	return fmt.Sprintf(`POD_CIDR='[%[5]q]' clusterctl generate cluster %[1]s --config %[6]s/clusterctl.yaml --from %[3]s \
  --kubernetes-version %[4]s --control-plane-machine-count 1 --worker-machine-count 1 --target-namespace %[2]s | kubectl apply -f - >/dev/null
kubectl -n %[2]s label cluster %[1]s cni=kindnet --overwrite >/dev/null`,
		name, namespace, providerPath("infrastructure-docker", "cluster-template-development.yaml"), infra.KubernetesVersion, podCIDR, repo)
}

// patchClusterClass puts every object in manifests into namespace, and adds a host mount
// to the node containers of every DevMachineTemplate.
func patchClusterClass(manifests []byte, namespace, hostPath, containerPath string) ([]byte, error) {
	docs := strings.Split(string(manifests), "\n---\n")
	patched := 0
	for i, doc := range docs {
		obj := map[string]any{}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, err
		}
		if err := unstructured.SetNestedField(obj, namespace, "metadata", "namespace"); err != nil {
			return nil, err
		}
		if obj["kind"] != "DevMachineTemplate" {
			out, err := yaml.Marshal(obj)
			if err != nil {
				return nil, err
			}
			docs[i] = string(out)
			continue
		}
		path := []string{"spec", "template", "spec", "backend", "docker", "extraMounts"}
		mounts, _, err := unstructured.NestedSlice(obj, path...)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, map[string]any{"hostPath": hostPath, "containerPath": containerPath})
		if err := unstructured.SetNestedSlice(obj, mounts, path...); err != nil {
			return nil, err
		}
		out, err := yaml.Marshal(obj)
		if err != nil {
			return nil, err
		}
		docs[i] = string(out)
		patched++
	}
	if patched == 0 {
		return nil, errors.New("no DevMachineTemplate to patch")
	}
	return []byte(strings.Join(docs, "\n---\n")), nil
}

// cniResourceSet installs kindnet into every Cluster labelled cni=kindnet.
func cniResourceSet(namespace, podCIDR string) ([]byte, error) {
	cm, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "kindnet", "namespace": namespace},
		"data":       map[string]any{"kindnet.yaml": strings.ReplaceAll(kindnet, "'${DOCKER_POD_CIDRS},${DOCKER_POD_IPV6_CIDRS}'", podCIDR)},
	})
	if err != nil {
		return nil, err
	}
	crs, err := yaml.Marshal(map[string]any{
		"apiVersion": "addons.cluster.x-k8s.io/v1beta2",
		"kind":       "ClusterResourceSet",
		"metadata":   map[string]any{"name": "kindnet", "namespace": namespace},
		"spec": map[string]any{
			"strategy":        "ApplyOnce",
			"clusterSelector": map[string]any{"matchLabels": map[string]any{"cni": "kindnet"}},
			"resources":       []any{map[string]any{"name": "kindnet", "kind": "ConfigMap"}},
		},
	})
	if err != nil {
		return nil, err
	}
	return append(append(cm, "---\n"...), crs...), nil
}
