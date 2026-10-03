// Command addon-manager installs the agent package into every CAPI workload cluster,
// with a version constraint and a values Secret, as Acme's production addon controller does.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var version string

var (
	clusters = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "clusters"}
	pkgis    = schema.GroupVersionResource{Group: "packaging.carvel.dev", Version: "v1alpha1", Resource: "packageinstalls"}
	secrets  = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

func main() {
	refName, constraint := os.Getenv("AGENT_REFNAME"), os.Getenv("AGENT_CONSTRAINT")
	if constraint == "self" {
		constraint = version
	}
	log.Printf("addon-manager version=%s agent=%s constraint=%s", version, refName, constraint)
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	dyn := dynamic.NewForConfigOrDie(cfg)
	ctx := context.Background()
	for {
		if err := reconcile(ctx, dyn, refName, constraint); err != nil {
			log.Print(err)
		}
		time.Sleep(5 * time.Second)
	}
}

func reconcile(ctx context.Context, dyn dynamic.Interface, refName, constraint string) error {
	list, err := dyn.Resource(clusters).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, c := range list.Items {
		ns, name := c.GetNamespace(), c.GetName()
		values := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Secret",
			"metadata":   map[string]any{"name": name + "-agent-values", "namespace": ns},
			"stringData": map[string]any{"values.yml": fmt.Sprintf("clusterName: %s\n", name)},
		}}
		pkgi := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "packaging.carvel.dev/v1alpha1", "kind": "PackageInstall",
			"metadata": map[string]any{"name": name + "-agent", "namespace": ns},
			"spec": map[string]any{
				"cluster": map[string]any{
					"namespace":           "default",
					"kubeconfigSecretRef": map[string]any{"name": name + "-kubeconfig", "key": "value"},
				},
				"packageRef": map[string]any{"refName": refName, "versionSelection": map[string]any{"constraints": constraint}},
				"values":     []any{map[string]any{"secretRef": map[string]any{"name": name + "-agent-values"}}},
				"noopDelete": true,
			},
		}}
		if err := upsert(ctx, dyn.Resource(secrets).Namespace(ns), values, "stringData"); err != nil {
			return err
		}
		if err := upsert(ctx, dyn.Resource(pkgis).Namespace(ns), pkgi, "spec"); err != nil {
			return err
		}
	}
	return nil
}

func upsert(ctx context.Context, r dynamic.ResourceInterface, want *unstructured.Unstructured, field string) error {
	got, err := r.Get(ctx, want.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = r.Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	got.Object[field] = want.Object[field]
	_, err = r.Update(ctx, got, metav1.UpdateOptions{})
	return err
}
