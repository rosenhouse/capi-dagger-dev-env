// Command greeting-syncer copies management-cluster Greetings into workload clusters.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/api/v1alpha1"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/greetingsyncer"
)

func main() {
	r := &greetingsyncer.Reconciler{}
	flag.StringVar(&r.TargetNamespace, "target-namespace", "default", "namespace in the workload cluster that receives Greetings")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("greeting-syncer")
	ctx := ctrl.SetupSignalHandler()

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	_ = demov1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}
	cc, err := clustercache.SetupWithManager(ctx, mgr, clustercache.Options{
		SecretClient: mgr.GetAPIReader(),
		Client:       clustercache.ClientOptions{UserAgent: "greeting-syncer"},
	}, controller.Options{})
	if err != nil {
		log.Error(err, "set up cluster cache")
		os.Exit(1)
	}
	r.Client = mgr.GetClient()
	r.Remote = cc
	if err := r.SetupWithManager(mgr, cc); err != nil {
		log.Error(err, "set up controller")
		os.Exit(1)
	}
	if err := mgr.Start(ctx); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
