package kube_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

func TestClusterAvailable(t *testing.T) {
	dyn := clusters(cluster("work", "True", ""))
	if err := kube.ClusterAvailable(context.Background(), dyn, "default", "work"); err != nil {
		t.Error(err)
	}
}

func TestClusterAvailableReportsConditionMessage(t *testing.T) {
	dyn := clusters(cluster("work", "False", "* ControlPlaneAvailable: control plane not yet initialized"))

	err := kube.ClusterAvailable(context.Background(), dyn, "default", "work")

	if err == nil || err.Error() != "Cluster default/work not Available: * ControlPlaneAvailable: control plane not yet initialized" {
		t.Errorf("err = %v", err)
	}
}

func TestClusterAvailableFailsForMissingCluster(t *testing.T) {
	if err := kube.ClusterAvailable(context.Background(), clusters(), "default", "work"); err == nil {
		t.Error("no error")
	}
}

func cluster(name, available, message string) runtime.Object {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Available", "status": available, "message": message}},
		},
	}}
}

func clusters(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.ClusterGVR: "ClusterList"}, objs...)
}
