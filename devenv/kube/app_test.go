package kube_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/kube"
)

func TestAppDeployedWhenItReconciledTheBundle(t *testing.T) {
	dyn := apps(app("reg/b@sha256:new", 2, 2, "ReconcileSucceeded"))
	if err := kube.AppDeployed(context.Background(), dyn, "devenv", "a", "reg/b@sha256:new"); err != nil {
		t.Error(err)
	}
}

func TestAppDeployedWaitsForTheNewBundle(t *testing.T) {
	dyn := apps(app("reg/b@sha256:old", 2, 2, "ReconcileSucceeded"))
	err := kube.AppDeployed(context.Background(), dyn, "devenv", "a", "reg/b@sha256:new")
	if err == nil || err.Error() != "App devenv/a fetches reg/b@sha256:old, not reg/b@sha256:new" {
		t.Errorf("err = %v", err)
	}
}

func TestAppDeployedWaitsForReconcileOfTheCurrentGeneration(t *testing.T) {
	dyn := apps(app("reg/b@sha256:new", 3, 2, "ReconcileSucceeded"))
	err := kube.AppDeployed(context.Background(), dyn, "devenv", "a", "reg/b@sha256:new")
	if err == nil || err.Error() != "App devenv/a not yet reconciled at generation 3" {
		t.Errorf("err = %v", err)
	}
}

func TestAppDeployedReportsFailure(t *testing.T) {
	failed := app("reg/b@sha256:new", 2, 2, "ReconcileFailed")
	unstructured.SetNestedField(failed.Object, "Reconcile failed: Deploying: Error", "status", "friendlyDescription")
	unstructured.SetNestedField(failed.Object, "kapp: Error: timed out waiting", "status", "usefulErrorMessage")
	err := kube.AppDeployed(context.Background(), apps(failed), "devenv", "a", "reg/b@sha256:new")
	if err == nil || err.Error() != "App devenv/a: Reconcile failed: Deploying: Error: kapp: Error: timed out waiting" {
		t.Errorf("err = %v", err)
	}
}

func TestAppDeployedReportsReconciling(t *testing.T) {
	reconciling := app("reg/b@sha256:new", 2, 2, "Reconciling")
	unstructured.SetNestedField(reconciling.Object, "Reconciling", "status", "friendlyDescription")
	err := kube.AppDeployed(context.Background(), apps(reconciling), "devenv", "a", "reg/b@sha256:new")
	if err == nil || err.Error() != "App devenv/a: Reconciling" {
		t.Errorf("err = %v", err)
	}
}

func app(bundle string, generation, observed int64, condition string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kappctrl.k14s.io/v1alpha1",
		"kind":       "App",
		"metadata":   map[string]any{"name": "a", "namespace": "devenv", "generation": generation},
		"spec":       map[string]any{"fetch": []any{map[string]any{"imgpkgBundle": map[string]any{"image": bundle}}}},
		"status": map[string]any{
			"observedGeneration": observed,
			"conditions":         []any{map[string]any{"type": condition, "status": "True"}},
		},
	}}
}

func apps(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.AppGVR: "AppList"}, objs...)
}
