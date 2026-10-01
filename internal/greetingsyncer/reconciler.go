// Package greetingsyncer copies each management-cluster Greeting into the
// workload cluster named by its spec.clusterName. Deleting a Greeting leaves
// its copy in place.
package greetingsyncer

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/api/v1alpha1"
)

//go:generate go tool controller-gen rbac:roleName=greeting-syncer paths=./... output:rbac:dir=../../config/greeting-syncer

// +kubebuilder:rbac:groups=demo.example.com,resources=greetings,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// crdRetryInterval paces retries while greeting-controller's package installs the CRD remotely.
const crdRetryInterval = 10 * time.Second

type RemoteClients interface {
	GetUncachedClient(ctx context.Context, cluster client.ObjectKey) (client.Client, error)
}

type Reconciler struct {
	client.Client
	Remote          RemoteClients
	TargetNamespace string
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, cc clustercache.ClusterCache) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&demov1.Greeting{}).
		WatchesRawSource(cc.GetClusterSource("greeting-syncer", r.GreetingsForCluster)).
		Complete(r)
}

// GreetingsForCluster maps a Cluster to the Greetings that target it.
func (r *Reconciler) GreetingsForCluster(ctx context.Context, cluster client.Object) []ctrl.Request {
	greetings := &demov1.GreetingList{}
	if err := r.List(ctx, greetings, client.InNamespace(cluster.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "list Greetings")
		return nil
	}
	var requests []ctrl.Request
	for _, g := range greetings.Items {
		if g.Spec.ClusterName == cluster.GetName() {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&g)})
		}
	}
	return requests
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	g := &demov1.Greeting{}
	if err := r.Get(ctx, req.NamespacedName, g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if g.Spec.ClusterName == "" {
		return ctrl.Result{}, nil
	}

	remote, err := r.Remote.GetUncachedClient(ctx, client.ObjectKey{Namespace: g.Namespace, Name: g.Spec.ClusterName})
	if errors.Is(err, clustercache.ErrClusterNotConnected) {
		log.FromContext(ctx).V(1).Info("Waiting for workload cluster connection", "cluster", g.Spec.ClusterName)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	copied := &demov1.Greeting{}
	copied.Name, copied.Namespace = g.Name, r.TargetNamespace
	_, err = controllerutil.CreateOrUpdate(ctx, remote, copied, func() error {
		copied.Spec = demov1.GreetingSpec{Message: g.Spec.Message}
		return nil
	})
	if meta.IsNoMatchError(err) {
		log.FromContext(ctx).Info("Waiting for Greeting CRD in workload cluster", "cluster", g.Spec.ClusterName)
		return ctrl.Result{RequeueAfter: crdRetryInterval}, nil
	}
	return ctrl.Result{}, err
}
