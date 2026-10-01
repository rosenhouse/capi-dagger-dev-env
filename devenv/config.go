package devenv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
	"github.com/rosenhouse/capi-dagger-dev-env/devenv/bundle"
)

// Config describes the components a consumer develops in the environment.
type Config struct {
	// Root is the consumer's Go module. Empty means the module that holds the working directory.
	Root string
	// Commands are main packages under Root, such as "./cmd/hello".
	// Each becomes an image named after its directory, with the build's version in main.version.
	Commands []string
	// Images, if set, builds more images, by name, from the source snapshot that Commands build from.
	Images func(c *dagger.Client, src *dagger.Directory, version string) map[string]*dagger.Container
	// Packages are the Carvel packages built from Root and published to the management cluster.
	Packages []Package
	// Ready, if set, waits for the consumer's components once the workload cluster is up.
	Ready func(ctx context.Context, e *Environment) error
	// Test, if set, is what devenv test runs against the ready environment.
	Test func(ctx context.Context, e *Environment) error
}

// Package is a Carvel package: a bundle of ytt config whose images devenv builds and locks.
type Package struct {
	// Name names the bundle and, for a Management package, its PackageInstall and App.
	Name string
	// RefName is the Carvel package name.
	RefName string
	// Config is the package's config directory under Root.
	Config string
	// Images are the images the bundle locks, by name. The config refers to each by that name.
	Images []string
	// On is where devenv installs the package.
	On Target
}

// Target says which cluster devenv installs a package into.
type Target int

const (
	// Management packages run on the management cluster, where devenv installs them.
	Management Target = iota
	// Workload packages are only published; a consumer's controller installs them into workload clusters.
	Workload
)

func (c Config) validate() error {
	var errs []error
	if len(c.Packages) == 0 {
		errs = append(errs, errors.New("no packages"))
	}
	builders := map[string]string{}
	for _, command := range c.Commands {
		if !strings.HasPrefix(command, "./") {
			errs = append(errs, fmt.Errorf("command %s does not start with ./", command))
		}
		image := build.ImageName(command)
		if other, ok := builders[image]; ok {
			errs = append(errs, fmt.Errorf("commands %s and %s both build image %s", other, command, image))
		}
		builders[image] = command
	}
	names := map[string]bool{}
	for _, p := range c.Packages {
		if p.Name == "" {
			errs = append(errs, errors.New("a package has no name"))
		}
		if names[p.Name] {
			errs = append(errs, fmt.Errorf("two packages are called %s", p.Name))
		}
		names[p.Name] = true
		if p.RefName == "" {
			errs = append(errs, fmt.Errorf("package %s has no RefName", p.Name))
		}
		if p.Config == "" {
			errs = append(errs, fmt.Errorf("package %s has no Config", p.Name))
		}
		if p.On != Management && p.On != Workload {
			errs = append(errs, fmt.Errorf("package %s has an unknown target %d", p.Name, p.On))
		}
	}
	return errors.Join(errs...)
}

// checkPaths fails unless every command and package config directory exists under Root.
func (c Config) checkPaths() error {
	var errs []error
	for _, command := range c.Commands {
		if _, err := os.Stat(filepath.Join(c.Root, command)); err != nil {
			errs = append(errs, fmt.Errorf("command %s not found under %s", command, c.Root))
		}
	}
	for _, p := range c.Packages {
		if _, err := os.Stat(filepath.Join(c.Root, p.Config)); err != nil {
			errs = append(errs, fmt.Errorf("package %s's config %s not found under %s", p.Name, p.Config, c.Root))
		}
	}
	return errors.Join(errs...)
}

// images merges the commands' images with those of the Images hook, and checks that every package's images exist.
func (c Config) images(commands, hook map[string]*dagger.Container) (map[string]*dagger.Container, error) {
	images := maps.Clone(commands)
	if images == nil {
		images = map[string]*dagger.Container{}
	}
	for _, name := range slices.Sorted(maps.Keys(hook)) {
		if _, ok := images[name]; ok {
			return nil, fmt.Errorf("Images builds %s, which a command already builds", name)
		}
		images[name] = hook[name]
	}
	for _, p := range c.Packages {
		for _, name := range p.Images {
			if _, ok := images[name]; !ok {
				return nil, fmt.Errorf("package %s: nothing builds image %q", p.Name, name)
			}
		}
	}
	return images, nil
}

// packageInstalls installs the Management packages with the installer service account.
func (c Config) packageInstalls() ([][]byte, error) {
	var pkgis [][]byte
	for _, p := range c.Packages {
		if p.On != Management {
			continue
		}
		pkgi, err := bundle.PackageInstall(p.Name, p.RefName, packageVersion, installerNamespace, installerServiceAccount)
		if err != nil {
			return nil, err
		}
		pkgis = append(pkgis, pkgi)
	}
	return pkgis, nil
}

// bundlesOn returns the bundles, from bundles by package name, of the packages on target.
func (c Config) bundlesOn(target Target, bundles map[string]string) []string {
	var refs []string
	for _, p := range c.Packages {
		if p.On == target {
			refs = append(refs, bundles[p.Name])
		}
	}
	return refs
}
