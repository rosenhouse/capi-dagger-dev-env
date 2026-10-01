// Package addonmanager installs a Carvel package into each CAPI workload
// cluster once its control plane is initialized.
package addonmanager

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

//go:generate go tool controller-gen rbac:roleName=addon-manager paths=./... output:rbac:dir=../../config/addon-manager

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=packaging.carvel.dev,resources=packageinstalls,verbs=get;list;watch;create;update

var PackageInstallGVK = schema.GroupVersionKind{Group: "packaging.carvel.dev", Version: "v1alpha1", Kind: "PackageInstall"}

type Reconciler struct {
	client.Client
	PackageName       string
	VersionConstraint string
	TargetNamespace   string
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.Cluster{}).
		Owns(newPackageInstall()).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cluster.DeletionTimestamp.IsZero() || !ptr.Deref(cluster.Status.Initialization.ControlPlaneInitialized, false) {
		return ctrl.Result{}, nil
	}

	shortName, _, _ := strings.Cut(r.PackageName, ".")
	pkgi := newPackageInstall()
	pkgi.SetNamespace(cluster.Namespace)
	pkgi.SetName(cluster.Name + "-" + shortName)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pkgi, func() error {
		pkgi.Object["spec"] = map[string]any{
			"cluster": map[string]any{
				"namespace": r.TargetNamespace,
				"kubeconfigSecretRef": map[string]any{
					"name": secret.Name(cluster.Name, secret.Kubeconfig),
					"key":  secret.KubeconfigDataName,
				},
			},
			"packageRef": map[string]any{
				"refName":          r.PackageName,
				"versionSelection": map[string]any{"constraints": r.VersionConstraint},
			},
			// The workload cluster may be gone by the time this PackageInstall is deleted.
			"noopDelete": true,
		}
		return controllerutil.SetControllerReference(cluster, pkgi, r.Scheme())
	})
	return ctrl.Result{}, err
}

func newPackageInstall() *unstructured.Unstructured {
	pkgi := &unstructured.Unstructured{}
	pkgi.SetGroupVersionKind(PackageInstallGVK)
	return pkgi
}
