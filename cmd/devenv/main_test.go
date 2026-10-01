package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
)

// Every unpinned image a package's config references must be locked by that package, and vice versa.
func TestPackageImagesMatchConfigPlaceholders(t *testing.T) {
	for _, p := range config.Packages {
		got, err := bundle.Placeholders(filepath.Join("..", "..", p.Config))
		if err != nil {
			t.Fatal(err)
		}
		want := slices.Sorted(slices.Values(p.Images))
		if !slices.Equal(got, want) {
			t.Errorf("%s: config placeholders %v, package images %v", p.Name, got, want)
		}
	}
}
