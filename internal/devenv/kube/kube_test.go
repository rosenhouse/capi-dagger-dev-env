package kube_test

import (
	"context"
	"strings"
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
	cs := fake.NewClientset(node("a", corev1.ConditionTrue), node("b", corev1.ConditionFalse))
	err := kube.NodesReady(context.Background(), cs)
	if err == nil || !strings.Contains(err.Error(), "b") || strings.Contains(err.Error(), "a,") {
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
