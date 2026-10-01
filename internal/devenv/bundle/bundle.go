// Package bundle renders the image locks of first-party imgpkg bundles, and the Packages that install them.
package bundle

import (
	"maps"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// GlobalNamespace makes Packages visible to PackageInstalls in every namespace.
const GlobalNamespace = "kapp-controller-packaging-global"

// ImagesLock renders .imgpkg/images.yml. Keys are the placeholder image names used in config/.
func ImagesLock(refs map[string]string) ([]byte, error) {
	type imageRef struct {
		Image       string            `json:"image"`
		Annotations map[string]string `json:"annotations"`
	}
	lock := struct {
		APIVersion string     `json:"apiVersion"`
		Kind       string     `json:"kind"`
		Images     []imageRef `json:"images"`
	}{APIVersion: "imgpkg.carvel.dev/v1alpha1", Kind: "ImagesLock"}
	for _, placeholder := range slices.Sorted(maps.Keys(refs)) {
		lock.Images = append(lock.Images, imageRef{
			Image:       refs[placeholder],
			Annotations: map[string]string{"kbld.carvel.dev/id": placeholder},
		})
	}
	return yaml.Marshal(lock)
}

// Package renders a Package whose template fetches bundleRef and resolves its images with kbld.
func Package(refName, version, bundleRef string) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"apiVersion": "data.packaging.carvel.dev/v1alpha1",
		"kind":       "Package",
		"metadata":   map[string]any{"name": refName + "." + version, "namespace": GlobalNamespace},
		"spec": map[string]any{
			"refName": refName,
			"version": version,
			"template": map[string]any{"spec": map[string]any{
				"fetch":    []any{map[string]any{"imgpkgBundle": map[string]any{"image": bundleRef}}},
				"template": []any{map[string]any{"ytt": map[string]any{"paths": []any{"config"}}}, map[string]any{"kbld": map[string]any{"paths": []any{"-", ".imgpkg/images.yml"}}}},
				"deploy":   []any{map[string]any{"kapp": map[string]any{}}},
			}},
		},
	})
}

// PackageInstall renders a PackageInstall named after the package's short name.
func PackageInstall(refName, version, namespace, serviceAccount string) ([]byte, error) {
	name, _, _ := strings.Cut(refName, ".")
	return yaml.Marshal(map[string]any{
		"apiVersion": "packaging.carvel.dev/v1alpha1",
		"kind":       "PackageInstall",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"serviceAccountName": serviceAccount,
			"packageRef": map[string]any{
				"refName":          refName,
				"versionSelection": map[string]any{"constraints": version},
			},
		},
	})
}
