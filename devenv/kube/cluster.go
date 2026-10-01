package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var ClusterGVR = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "clusters"}

// ClusterAvailable returns nil when the CAPI Cluster's Available condition is true, and its message otherwise.
func ClusterAvailable(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	cluster, err := dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if hasTrueCondition(*cluster, "Available") {
		return nil
	}
	return fmt.Errorf("Cluster %s/%s not Available: %s", namespace, name, conditionMessage(*cluster, "Available"))
}

func conditionMessage(obj unstructured.Unstructured, conditionType string) string {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		if c, _ := c.(map[string]any); c["type"] == conditionType {
			msg, _ := c["message"].(string)
			return msg
		}
	}
	return "no " + conditionType + " condition"
}
