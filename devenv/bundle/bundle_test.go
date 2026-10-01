package bundle_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
)

func TestImagesLockMapsPlaceholdersToRefs(t *testing.T) {
	out, err := bundle.ImagesLock(map[string]string{
		"hello":         "reg.local:5000/hello@sha256:bbb",
		"addon-manager": "reg.local:5000/addon-manager@sha256:aaa",
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "imgpkg.carvel.dev/v1alpha1",
		"kind":       "ImagesLock",
		"images": []any{
			map[string]any{"image": "reg.local:5000/addon-manager@sha256:aaa", "annotations": map[string]any{"kbld.carvel.dev/id": "addon-manager"}},
			map[string]any{"image": "reg.local:5000/hello@sha256:bbb", "annotations": map[string]any{"kbld.carvel.dev/id": "hello"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestPackageFetchesBundleAndResolvesImagesWithKbld(t *testing.T) {
	out, err := bundle.Package("greeting-controller.demo.example.com", "0.1.0", "reg.local:5000/bundles/greeting-controller@sha256:ccc")
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "data.packaging.carvel.dev/v1alpha1",
		"kind":       "Package",
		"metadata":   map[string]any{"name": "greeting-controller.demo.example.com.0.1.0", "namespace": "kapp-controller-packaging-global"},
		"spec": map[string]any{
			"refName": "greeting-controller.demo.example.com",
			"version": "0.1.0",
			"template": map[string]any{"spec": map[string]any{
				"fetch":    []any{map[string]any{"imgpkgBundle": map[string]any{"image": "reg.local:5000/bundles/greeting-controller@sha256:ccc"}}},
				"template": []any{map[string]any{"ytt": map[string]any{"paths": []any{"config"}}}, map[string]any{"kbld": map[string]any{"paths": []any{"-", ".imgpkg/images.yml"}}}},
				"deploy":   []any{map[string]any{"kapp": map[string]any{}}},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestPackageInstallPinsVersionAndServiceAccount(t *testing.T) {
	out, err := bundle.PackageInstall("manager", "addon-manager.demo.example.com", "0.1.0", "devenv", "devenv-installer")
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "packaging.carvel.dev/v1alpha1",
		"kind":       "PackageInstall",
		"metadata":   map[string]any{"name": "manager", "namespace": "devenv"},
		"spec": map[string]any{
			"serviceAccountName": "devenv-installer",
			"packageRef": map[string]any{
				"refName":          "addon-manager.demo.example.com",
				"versionSelection": map[string]any{"constraints": "0.1.0"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestPlaceholdersFindsTheImagesKbldResolves(t *testing.T) {
	dir := t.TempDir()
	config := `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: controller
      - image: nginx@sha256:0123
---
apiVersion: v1
kind: ConfigMap
data:
  helloImage: hello
  proxyImage: nginx@sha256:0123
---
apiVersion: kbld.k14s.io/v1alpha1
kind: Config
searchRules:
- keyMatcher:
    name: helloImage
- keyMatcher:
    name: proxyImage
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := bundle.Placeholders(dir)

	if err != nil || !slices.Equal(got, []string{"controller", "hello"}) {
		t.Errorf("Placeholders() = %v, %v", got, err)
	}
}
