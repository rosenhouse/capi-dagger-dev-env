package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var AppGVR = schema.GroupVersionResource{Group: "kappctrl.k14s.io", Version: "v1alpha1", Resource: "apps"}

// BundleAppsDeployed returns nil once every kapp-controller App, in any namespace, that fetches from the repository
// of a bundle in required or optional fetches that bundle and has reconciled its current generation.
// It fails while no App fetches a required bundle.
func BundleAppsDeployed(ctx context.Context, dyn dynamic.Interface, required, optional []string) error {
	want := map[string]string{}
	for _, bundle := range slices.Concat(required, optional) {
		repo, _, _ := strings.Cut(bundle, "@")
		want[repo] = bundle
	}
	list, err := dyn.Resource(AppGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	fetching := map[string]bool{}
	var errs []error
	for _, app := range list.Items {
		for _, fetched := range fetchedBundles(app) {
			repo, _, _ := strings.Cut(fetched, "@")
			bundle, ok := want[repo]
			if !ok {
				continue
			}
			fetching[bundle] = true
			errs = append(errs, appDeployed(app, fetched, bundle))
		}
	}
	for _, bundle := range required {
		if !fetching[bundle] {
			errs = append(errs, fmt.Errorf("no App fetches %s", bundle))
		}
	}
	return errors.Join(errs...)
}

func fetchedBundles(app unstructured.Unstructured) []string {
	fetch, _, _ := unstructured.NestedSlice(app.Object, "spec", "fetch")
	var images []string
	for _, f := range fetch {
		step, _ := f.(map[string]any)
		if image, ok, _ := unstructured.NestedString(step, "imgpkgBundle", "image"); ok {
			images = append(images, image)
		}
	}
	return images
}

func appDeployed(app unstructured.Unstructured, fetched, bundle string) error {
	namespace, name := app.GetNamespace(), app.GetName()
	if fetched != bundle {
		return fmt.Errorf("App %s/%s fetches %s, not %s", namespace, name, fetched, bundle)
	}
	observed, _, _ := unstructured.NestedInt64(app.Object, "status", "observedGeneration")
	if observed != app.GetGeneration() {
		return fmt.Errorf("App %s/%s not yet reconciled at generation %d", namespace, name, app.GetGeneration())
	}
	if !hasTrueCondition(app, "ReconcileSucceeded") {
		msg, _, _ := unstructured.NestedString(app.Object, "status", "friendlyDescription")
		if useful, _, _ := unstructured.NestedString(app.Object, "status", "usefulErrorMessage"); useful != "" {
			msg += ": " + useful
		}
		return fmt.Errorf("App %s/%s: %s", namespace, name, msg)
	}
	return nil
}
