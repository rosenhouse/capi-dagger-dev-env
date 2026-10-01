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

// PackageGVR is served by kapp-controller itself, through an aggregated API.
var PackageGVR = schema.GroupVersionResource{Group: "data.packaging.carvel.dev", Version: "v1alpha1", Resource: "packages"}

// PackagesServed returns nil when kapp-controller answers for Packages.
func PackagesServed(ctx context.Context, dyn dynamic.Interface) error {
	_, err := dyn.Resource(PackageGVR).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

// PackageInstallReconciled returns nil when the named PackageInstall reports ReconcileSucceeded.
func PackageInstallReconciled(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	pkgi, err := dyn.Resource(PackageInstallGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if hasTrueCondition(*pkgi, "ReconcileSucceeded") {
		return nil
	}
	msg, _, _ := unstructured.NestedString(pkgi.Object, "status", "usefulErrorMessage")
	return fmt.Errorf("PackageInstall %s/%s not reconciled: %s", namespace, name, msg)
}
