package kube_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

func TestNodesReadyWhenEveryNodeIsReady(t *testing.T) {
	cs := fake.NewClientset(node("a", corev1.ConditionTrue), node("b", corev1.ConditionTrue))
	if err := kube.NodesReady(context.Background(), cs); err != nil {
		t.Error(err)
	}
}

func TestNodesReadyNamesNodesThatAreNotReady(t *testing.T) {
	cs := fake.NewClientset(node("ready-node", corev1.ConditionTrue), node("sick-node", corev1.ConditionFalse))
	err := kube.NodesReady(context.Background(), cs)
	if err == nil || err.Error() != "nodes not ready: sick-node" {
		t.Errorf("err = %v", err)
	}
}

func TestNodesReadyFailsWithoutNodes(t *testing.T) {
	if err := kube.NodesReady(context.Background(), fake.NewClientset()); err == nil {
		t.Error("no error for a cluster without nodes")
	}
}

func node(name string, ready corev1.ConditionStatus) runtime.Object {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}},
	}
}

func TestWithServerRewritesEveryCluster(t *testing.T) {
	in := clientcmdapi.NewConfig()
	in.Clusters["a"] = &clientcmdapi.Cluster{Server: "https://0.0.0.0:6443", CertificateAuthorityData: []byte("ca")}
	raw, _ := clientcmd.Write(*in)

	out, err := kube.WithServer(raw, "https://docker:6443", "localhost")
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ := clientcmd.Load(out)
	if c := cfg.Clusters["a"]; c.Server != "https://docker:6443" || c.TLSServerName != "localhost" || string(c.CertificateAuthorityData) != "ca" {
		t.Errorf("cluster = %+v", c)
	}
}
