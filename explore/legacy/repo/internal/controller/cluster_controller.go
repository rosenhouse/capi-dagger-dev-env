// Package controller reconciles CAPI Clusters by configuring each workload cluster.
package controller

import (
	"context"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/acme/fleet-common/labels"

	fleetv1 "github.com/acme/fleet-addons/api/v1alpha1"
	"github.com/acme/fleet-addons/internal/version"
)

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=fleet.acme.io,resources=fleetpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

const agentNamespace = "fleet-system"

type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cluster.Labels[labels.Managed] == "false" {
		return ctrl.Result{}, nil
	}
	if !conditions.IsTrue(cluster, clusterv1.ControlPlaneInitializedCondition) {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	cfg, err := remote.RESTConfig(ctx, "fleet-addons", r.Client, client.ObjectKeyFromObject(cluster))
	if err != nil {
		return ctrl.Result{}, err
	}
	workload, err := client.New(cfg, client.Options{Scheme: r.Scheme})
	if err != nil {
		return ctrl.Result{}, err
	}
	message := ""
	policy := &fleetv1.FleetPolicy{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: "default"}, policy); err == nil {
		message = policy.Spec.Message
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, workload, ns, func() error { return nil }); err != nil {
		return ctrl.Result{}, err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agentNamespace, Name: "fleet-info"}}
	if _, err := controllerutil.CreateOrUpdate(ctx, workload, cm, func() error {
		cm.Data = map[string]string{"version": version.Version, "message": message}
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	if image := os.Getenv("AGENT_IMAGE"); image != "" {
		if err := r.ensureAgent(ctx, workload, image); err != nil {
			return ctrl.Result{}, err
		}
	}
	logger.Info("configured workload cluster", "version", version.Version)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *ClusterReconciler) ensureAgent(ctx context.Context, workload client.Client, image string) error {
	selector := map[string]string{"app": "fleet-agent"}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: agentNamespace, Name: "fleet-agent"}}
	_, err := controllerutil.CreateOrUpdate(ctx, workload, d, func() error {
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		d.Spec.Template.Labels = selector
		d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "agent", Image: image}}
		return nil
	})
	return err
}

func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&clusterv1.Cluster{}).Complete(r)
}
