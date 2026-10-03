// Command rootfsprobe exports containers whose rootfs is a subdirectory of the source, as an Images hook builds them.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"dagger.io/dagger"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/build"
)

func main() {
	ctx := context.Background()
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(io.Discard))
	if err != nil {
		panic(err)
	}
	defer c.Close()
	root, _ := os.Getwd()
	src, err := build.Source(c, root).Sync(ctx)
	if err != nil {
		panic(err)
	}
	variants := map[string]*dagger.Container{
		"synced-subdir": c.Container().WithRootfs(src.Directory("deploy/agent")),
		"host-subdir":   c.Container().WithRootfs(c.Host().Directory(root).Directory("deploy/agent")),
		"copied-subdir": c.Container().WithRootfs(c.Directory().WithDirectory(".", src.Directory("deploy/agent"))),
	}
	for name, ctr := range variants {
		entries, err := ctr.Rootfs().Entries(ctx)
		fmt.Printf("OBS: %s Rootfs().Entries() = %v %v\n", name, entries, err)
		if _, err := ctr.AsTarball().Export(ctx, os.Args[1]+"/"+name+".tar"); err != nil {
			panic(err)
		}
	}
}
