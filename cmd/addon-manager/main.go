// Command addon-manager installs greeting-controller into each CAPI workload cluster.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/addonmanager"
)

func main() {
	r := &addonmanager.Reconciler{}
	flag.StringVar(&r.PackageName, "package-name", "greeting-controller.demo.example.com", "refName of the package to install")
	flag.StringVar(&r.VersionConstraint, "package-version", ">=0.0.0", "version constraint for the package")
	flag.StringVar(&r.TargetNamespace, "target-namespace", "default", "namespace in the workload cluster for the package's app record")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("addon-manager")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}
	r.Client = mgr.GetClient()
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "set up controller")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
