package addonmanager_test

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/addonmanager"
)

func TestInstallsPackageIntoInitializedCluster(t *testing.T) {
	c, r := setup(cluster(true))
	reconcile(t, r)

	spec := packageInstall(t, c).Object["spec"]
	want := map[string]any{
		"cluster": map[string]any{
			"namespace":           "demo-system",
			"kubeconfigSecretRef": map[string]any{"name": "work-kubeconfig", "key": "value"},
		},
		"packageRef": map[string]any{
			"refName":          "greeting-controller.demo.example.com",
			"versionSelection": map[string]any{"constraints": ">=0.0.0"},
		},
		"noopDelete": true,
	}
	if !reflect.DeepEqual(spec, want) {
		t.Errorf("spec = %v\nwant %v", spec, want)
	}
}

func TestPackageInstallIsOwnedByCluster(t *testing.T) {
	c, r := setup(cluster(true))
	reconcile(t, r)

	refs := packageInstall(t, c).GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Cluster" || refs[0].UID != "cluster-uid" {
		t.Errorf("owner references = %v", refs)
	}
}

func TestRestoresModifiedSpec(t *testing.T) {
	c, r := setup(cluster(true))
	reconcile(t, r)
	pkgi := packageInstall(t, c)
	_ = unstructured.SetNestedField(pkgi.Object, "other.example.com", "spec", "packageRef", "refName")
	if err := c.Update(context.Background(), pkgi); err != nil {
		t.Fatal(err)
	}

	reconcile(t, r)

	refName, _, _ := unstructured.NestedString(packageInstall(t, c).Object, "spec", "packageRef", "refName")
	if refName != "greeting-controller.demo.example.com" {
		t.Errorf("refName = %q", refName)
	}
}

func TestWaitsForControlPlaneInitialization(t *testing.T) {
	c, r := setup(cluster(false))
	reconcile(t, r)

	pkgi := &unstructured.Unstructured{}
	pkgi.SetGroupVersionKind(addonmanager.PackageInstallGVK)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "work-greeting-controller"}, pkgi)
	if !apierrors.IsNotFound(err) {
		t.Errorf("get PackageInstall: err = %v, want NotFound", err)
	}
}

func TestIgnoresMissingCluster(t *testing.T) {
	_, r := setup()
	reconcile(t, r)
}

func cluster(initialized bool) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "work", UID: "cluster-uid"},
		Status: clusterv1.ClusterStatus{Initialization: clusterv1.ClusterInitializationStatus{
			ControlPlaneInitialized: ptr.To(initialized),
		}},
	}
}

func setup(objs ...client.Object) (client.Client, *addonmanager.Reconciler) {
	s := runtime.NewScheme()
	_ = clusterv1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(objs...).Build()
	return c, &addonmanager.Reconciler{
		Client:            c,
		PackageName:       "greeting-controller.demo.example.com",
		VersionConstraint: ">=0.0.0",
		TargetNamespace:   "demo-system",
	}
}

func reconcile(t *testing.T, r *addonmanager.Reconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "work"}}); err != nil {
		t.Fatal(err)
	}
}

func packageInstall(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	pkgi := &unstructured.Unstructured{}
	pkgi.SetGroupVersionKind(addonmanager.PackageInstallGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "work-greeting-controller"}, pkgi); err != nil {
		t.Fatal(err)
	}
	return pkgi
}
