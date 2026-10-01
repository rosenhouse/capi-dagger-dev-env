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

const newBundle = "reg/bundles/a@sha256:new"

func TestBundleAppsDeployedWhenEveryAppReconciledItsBundle(t *testing.T) {
	dyn := apps(app("devenv", "a", newBundle, 2, 2, "ReconcileSucceeded"), app("default", "work-a", newBundle, 1, 1, "ReconcileSucceeded"))
	if err := kube.BundleAppsDeployed(context.Background(), dyn, []string{newBundle}); err != nil {
		t.Error(err)
	}
}

func TestBundleAppsDeployedWaitsForAnAppStillOnTheOldBundle(t *testing.T) {
	dyn := apps(app("devenv", "a", newBundle, 2, 2, "ReconcileSucceeded"), app("default", "work-a", "reg/bundles/a@sha256:old", 1, 1, "ReconcileSucceeded"))
	err := kube.BundleAppsDeployed(context.Background(), dyn, []string{newBundle})
	if err == nil || err.Error() != "App default/work-a fetches reg/bundles/a@sha256:old, not "+newBundle {
		t.Errorf("err = %v", err)
	}
}

func TestBundleAppsDeployedIgnoresAppsOfOtherBundles(t *testing.T) {
	dyn := apps(app("devenv", "a", newBundle, 2, 2, "ReconcileSucceeded"), app("other", "b", "elsewhere/b@sha256:old", 1, 0, "Reconciling"))
	if err := kube.BundleAppsDeployed(context.Background(), dyn, []string{newBundle}); err != nil {
		t.Error(err)
	}
}

func TestBundleAppsDeployedNeedsAnApp(t *testing.T) {
	err := kube.BundleAppsDeployed(context.Background(), apps(), []string{newBundle})
	if err == nil || err.Error() != "no App fetches "+newBundle {
		t.Errorf("err = %v", err)
	}
}

func TestBundleAppsDeployedWaitsForReconcileOfTheCurrentGeneration(t *testing.T) {
	dyn := apps(app("devenv", "a", newBundle, 3, 2, "ReconcileSucceeded"))
	err := kube.BundleAppsDeployed(context.Background(), dyn, []string{newBundle})
	if err == nil || err.Error() != "App devenv/a not yet reconciled at generation 3" {
		t.Errorf("err = %v", err)
	}
}

func TestBundleAppsDeployedReportsFailure(t *testing.T) {
	failed := app("devenv", "a", newBundle, 2, 2, "ReconcileFailed")
	unstructured.SetNestedField(failed.Object, "Reconcile failed: Deploying: Error", "status", "friendlyDescription")
	unstructured.SetNestedField(failed.Object, "kapp: Error: timed out waiting", "status", "usefulErrorMessage")
	err := kube.BundleAppsDeployed(context.Background(), apps(failed), []string{newBundle})
	if err == nil || err.Error() != "App devenv/a: Reconcile failed: Deploying: Error: kapp: Error: timed out waiting" {
		t.Errorf("err = %v", err)
	}
}

func TestBundleAppsDeployedReportsReconciling(t *testing.T) {
	reconciling := app("devenv", "a", newBundle, 2, 2, "Reconciling")
	unstructured.SetNestedField(reconciling.Object, "Reconciling", "status", "friendlyDescription")
	err := kube.BundleAppsDeployed(context.Background(), apps(reconciling), []string{newBundle})
	if err == nil || err.Error() != "App devenv/a: Reconciling" {
		t.Errorf("err = %v", err)
	}
}

func app(namespace, name, bundle string, generation, observed int64, condition string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kappctrl.k14s.io/v1alpha1",
		"kind":       "App",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "generation": generation},
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
