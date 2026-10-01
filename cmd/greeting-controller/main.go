// Command greeting-controller deploys hello behind nginx for each Greeting.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/api/v1alpha1"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/greetingcontroller"
)

func main() {
	r := &greetingcontroller.Reconciler{}
	flag.StringVar(&r.HelloImage, "hello-image", os.Getenv("HELLO_IMAGE"), "image for the hello app")
	flag.StringVar(&r.ProxyImage, "proxy-image", os.Getenv("PROXY_IMAGE"), "image for the nginx proxy")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("greeting-controller")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = demov1.AddToScheme(scheme)
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
