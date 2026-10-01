package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var MachineHealthCheckGVR = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "machinehealthchecks"}

// MachinesHealthy returns nil when every MachineHealthCheck in namespace counts all the machines it expects as healthy.
func MachinesHealthy(ctx context.Context, dyn dynamic.Interface, namespace string) error {
	list, err := dyn.Resource(MachineHealthCheckGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, mhc := range list.Items {
		expected, counted, _ := unstructured.NestedInt64(mhc.Object, "status", "expectedMachines")
		healthy, _, _ := unstructured.NestedInt64(mhc.Object, "status", "currentHealthy")
		switch {
		case !counted:
			return fmt.Errorf("MachineHealthCheck %s/%s has not counted its machines", namespace, mhc.GetName())
		case healthy != expected:
			return fmt.Errorf("MachineHealthCheck %s/%s counts %d of %d machines healthy", namespace, mhc.GetName(), healthy, expected)
		}
	}
	return nil
}
