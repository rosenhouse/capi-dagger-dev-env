package devenv

import (
	"context"
	"errors"
	"fmt"

	"dagger.io/dagger"
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
	if len(c.Packages) == 0 {
		return errors.New("no packages")
	}
	names := map[string]bool{}
	var errs []error
	for _, p := range c.Packages {
		switch {
		case p.Name == "":
			errs = append(errs, errors.New("a package has no name"))
		case p.RefName == "":
			errs = append(errs, fmt.Errorf("package %s has no RefName", p.Name))
		case p.Config == "":
			errs = append(errs, fmt.Errorf("package %s has no Config", p.Name))
		case names[p.Name]:
			errs = append(errs, fmt.Errorf("two packages are called %s", p.Name))
		}
		names[p.Name] = true
	}
	return errors.Join(errs...)
}
