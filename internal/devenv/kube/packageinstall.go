package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var PackageInstallGVR = schema.GroupVersionResource{Group: "packaging.carvel.dev", Version: "v1alpha1", Resource: "packageinstalls"}

// PackageInstallsReconciled returns nil when the namespace has PackageInstalls and all of them report ReconcileSucceeded.
func PackageInstallsReconciled(ctx context.Context, dyn dynamic.Interface, namespace string) error {
	list, err := dyn.Resource(PackageInstallGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return errors.New("no PackageInstalls")
	}
	var pending []string
	for _, pkgi := range list.Items {
		if !hasTrueCondition(pkgi, "ReconcileSucceeded") {
			msg, _, _ := unstructured.NestedString(pkgi.Object, "status", "usefulErrorMessage")
			pending = append(pending, strings.TrimSpace(fmt.Sprintf("%s %s", pkgi.GetName(), msg)))
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("PackageInstalls not reconciled: %s", strings.Join(pending, "; "))
	}
	return nil
}

func hasTrueCondition(obj unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		c, _ := c.(map[string]any)
		if c["type"] == conditionType && c["status"] == "True" {
			return true
		}
	}
	return false
}
