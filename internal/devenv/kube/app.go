package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var AppGVR = schema.GroupVersionResource{Group: "kappctrl.k14s.io", Version: "v1alpha1", Resource: "apps"}

// AppDeployed returns nil once the kapp-controller App fetches bundle and has reconciled its current generation.
// A PackageInstall's App shares its name and namespace.
func AppDeployed(ctx context.Context, dyn dynamic.Interface, namespace, name, bundle string) error {
	app, err := dyn.Resource(AppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	fetch, _, _ := unstructured.NestedSlice(app.Object, "spec", "fetch")
	var fetched string
	if len(fetch) > 0 {
		fetched, _, _ = unstructured.NestedString(fetch[0].(map[string]any), "imgpkgBundle", "image")
	}
	if fetched != bundle {
		return fmt.Errorf("App %s/%s fetches %s, not %s", namespace, name, fetched, bundle)
	}
	observed, _, _ := unstructured.NestedInt64(app.Object, "status", "observedGeneration")
	if observed != app.GetGeneration() {
		return fmt.Errorf("App %s/%s not yet reconciled at generation %d", namespace, name, app.GetGeneration())
	}
	if !hasTrueCondition(*app, "ReconcileSucceeded") {
		msg, _, _ := unstructured.NestedString(app.Object, "status", "friendlyDescription")
		if useful, _, _ := unstructured.NestedString(app.Object, "status", "usefulErrorMessage"); useful != "" {
			msg += ": " + useful
		}
		return fmt.Errorf("App %s/%s: %s", namespace, name, msg)
	}
	return nil
}
