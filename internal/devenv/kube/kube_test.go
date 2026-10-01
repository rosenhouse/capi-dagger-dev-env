package kube_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

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

func TestRestartsCountsEveryContainerInEveryNamespace(t *testing.T) {
	pod := func(namespace, name string, restarts ...int32) runtime.Object {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
		for _, r := range restarts {
			p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{RestartCount: r})
		}
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{RestartCount: 1}}
		return p
	}
	cs := fake.NewClientset(pod("kube-system", "kube-apiserver", 2), pod("default", "hello", 0, 3))

	got, err := kube.Restarts(context.Background(), cs)

	if err != nil || got != 7 {
		t.Errorf("Restarts() = %d, %v; want 7", got, err)
	}
}

func node(name string, ready corev1.ConditionStatus) runtime.Object {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}},
	}
}
