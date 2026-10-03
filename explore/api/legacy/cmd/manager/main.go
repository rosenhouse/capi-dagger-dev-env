// Command manager is a legacy multi-cluster-aware controller.
package main

import (
	"context"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type reconciler struct {
	client.Client
	tracker *remote.ClusterCacheTracker
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c := &clusterv1.Cluster{}
	return ctrl.Result{}, client.IgnoreNotFound(r.Get(ctx, req.NamespacedName, c))
}

func main() {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		os.Exit(1)
	}
	r := &reconciler{Client: mgr.GetClient()}
	if err := ctrl.NewControllerManagedBy(mgr).For(&clusterv1.Cluster{}).Complete(r); err != nil {
		os.Exit(1)
	}
	_ = mgr.Start(ctrl.SetupSignalHandler())
}
