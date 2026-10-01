package platform

import (
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const clusterClass = `apiVersion: cluster.x-k8s.io/v1beta2
kind: ClusterClass
metadata:
  name: quick-start
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: DevMachineTemplate
metadata:
  name: quick-start-control-plane
spec:
  template:
    spec:
      backend:
        docker:
          extraMounts:
          - containerPath: /var/run/docker.sock
            hostPath: /var/run/docker.sock
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: DevMachineTemplate
metadata:
  name: quick-start-default-worker-machinetemplate
spec:
  template:
    spec:
      backend:
        docker: {}
`

func TestWithExtraMountAddsMountToEveryMachineTemplate(t *testing.T) {
	out, err := withExtraMount([]byte(clusterClass), "/etc/devenv/certs.d", "/etc/containerd/certs.d")
	if err != nil {
		t.Fatal(err)
	}

	docs := strings.Split(string(out), "\n---\n")
	if len(docs) != 3 {
		t.Fatalf("got %d documents", len(docs))
	}
	certs := map[string]any{"hostPath": "/etc/devenv/certs.d", "containerPath": "/etc/containerd/certs.d"}
	socket := map[string]any{"hostPath": "/var/run/docker.sock", "containerPath": "/var/run/docker.sock"}
	for i, want := range [][]any{{socket, certs}, {certs}} {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(docs[i+1]), &doc); err != nil {
			t.Fatal(err)
		}
		got := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["backend"].(map[string]any)["docker"].(map[string]any)["extraMounts"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("document %d extraMounts = %v, want %v", i+1, got, want)
		}
	}
}

func TestWithExtraMountFailsWithoutMachineTemplates(t *testing.T) {
	if _, err := withExtraMount([]byte("kind: ClusterClass\n"), "/a", "/b"); err == nil {
		t.Error("no error")
	}
}

func TestCNIResourceSetCarriesKindnetForPodCIDR(t *testing.T) {
	out, err := cniResourceSet("default", "192.168.0.0/16")
	if err != nil {
		t.Fatal(err)
	}

	s := string(out)
	for _, want := range []string{"kind: ClusterResourceSet", "cni: kindnet", "kindest/kindnetd", "192.168.0.0/16"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "${") {
		t.Error("unsubstituted variable")
	}
}
