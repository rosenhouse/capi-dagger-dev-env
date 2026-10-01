package kube_test

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

func TestPackageInstallsReconciledWhenAllSucceeded(t *testing.T) {
	dyn := fakeDynamic(packageInstall("a", "ReconcileSucceeded", ""), packageInstall("b", "ReconcileSucceeded", ""))
	if err := kube.PackageInstallsReconciled(context.Background(), dyn, "devenv"); err != nil {
		t.Error(err)
	}
}

func TestPackageInstallsReconciledReportsUsefulErrorMessage(t *testing.T) {
	dyn := fakeDynamic(packageInstall("a", "ReconcileSucceeded", ""), packageInstall("b", "ReconcileFailed", "fetch: connection refused"))

	err := kube.PackageInstallsReconciled(context.Background(), dyn, "devenv")

	if err == nil || !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "fetch: connection refused") {
		t.Errorf("err = %v", err)
	}
}

func TestPackageInstallsReconciledFailsWhenNoneExist(t *testing.T) {
	if err := kube.PackageInstallsReconciled(context.Background(), fakeDynamic(), "devenv"); err == nil {
		t.Error("no error")
	}
}

func packageInstall(name, condition, usefulErrorMessage string) runtime.Object {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "packaging.carvel.dev/v1alpha1",
		"kind":       "PackageInstall",
		"metadata":   map[string]any{"name": name, "namespace": "devenv"},
		"status": map[string]any{
			"conditions":         []any{map[string]any{"type": condition, "status": "True"}},
			"usefulErrorMessage": usefulErrorMessage,
		},
	}}
	return obj
}

func fakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.PackageInstallGVR: "PackageInstallList"}, objs...)
}

func TestPackagesServed(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.PackageGVR: "PackageList"})
	if err := kube.PackagesServed(context.Background(), dyn); err != nil {
		t.Error(err)
	}

	dyn.PrependReactor("list", "packages", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("kapp-controller is starting")
	})
	if err := kube.PackagesServed(context.Background(), dyn); err == nil || !strings.Contains(err.Error(), "kapp-controller is starting") {
		t.Errorf("err = %v", err)
	}
}

func TestPackageInstallReconciledChecksOnlyTheNamedInstall(t *testing.T) {
	dyn := fakeDynamic(packageInstall("other", "ReconcileFailed", "unrelated"), packageInstall("work-greeting-controller", "ReconcileSucceeded", ""))
	if err := kube.PackageInstallReconciled(context.Background(), dyn, "devenv", "work-greeting-controller"); err != nil {
		t.Error(err)
	}
	err := kube.PackageInstallReconciled(context.Background(), dyn, "devenv", "other")
	if err == nil || err.Error() != "PackageInstall devenv/other not reconciled: unrelated" {
		t.Errorf("err = %v", err)
	}
}
