// Package oci assembles first-party images and imgpkg bundles and pushes them to a registry.
package oci

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Base returns the image for platform from ref, which must be pinned by digest.
// The first call for a ref and platform pulls the image into an OCI layout under cacheDir.
// Later calls read only that layout.
func Base(ctx context.Context, ref string, platform v1.Platform, cacheDir string) (v1.Image, error) {
	d, err := name.NewDigest(ref)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, strings.NewReplacer(":", "-", "/", "-").Replace(d.DigestStr()+"-"+platform.String()))
	if img, err := cached(dir); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return nil, fmt.Errorf("cached base %s: %w", dir, err)
		}
		return img, nil
	}
	img, err := remote.Image(d, remote.WithContext(ctx), remote.WithPlatform(platform))
	if err != nil {
		return nil, err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	if got := cfg.Platform(); got == nil || !got.Satisfies(platform) {
		return nil, fmt.Errorf("%s is for %v, not %s", ref, got, platform)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(cacheDir, ".pull-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	p, err := layout.Write(tmp, empty.Index)
	if err != nil {
		return nil, err
	}
	if err := p.AppendImage(img); err != nil {
		return nil, err
	}
	// A concurrent call may have cached the same image first.
	if err := os.Rename(tmp, dir); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return cached(dir)
}

func cached(dir string) (v1.Image, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return nil, err
	}
	idx, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) != 1 {
		return nil, fmt.Errorf("holds %d images, want 1", len(m.Manifests))
	}
	return p.Image(m.Manifests[0].Digest)
}

// Image adds binary to base as /<command> and makes it the entrypoint.
func Image(base v1.Image, command string, binary []byte) (v1.Image, error) {
	mt, err := base.MediaType()
	if err != nil {
		return nil, err
	}
	layerType := types.DockerLayer
	if mt == types.OCIManifestSchema1 {
		layerType = types.OCILayer
	}
	l, err := layer([]file{{command, binary, 0o755}}, layerType)
	if err != nil {
		return nil, err
	}
	img, err := mutate.AppendLayers(base, l)
	if err != nil {
		return nil, err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	c := cfg.Config
	c.Entrypoint, c.Cmd = []string{"/" + command}, nil
	return mutate.Config(img, c)
}

// Bundle packs config, keyed by path below config/, and its images lock as an imgpkg bundle.
func Bundle(config map[string][]byte, imagesLock []byte) (v1.Image, error) {
	files := []file{{".imgpkg/images.yml", imagesLock, 0o644}}
	for path, content := range config {
		files = append(files, file{"config/" + path, content, 0o644})
	}
	l, err := layer(files, types.DockerLayer)
	if err != nil {
		return nil, err
	}
	img, err := mutate.AppendLayers(empty.Image, l)
	if err != nil {
		return nil, err
	}
	return mutate.Config(img, v1.Config{Labels: map[string]string{"dev.carvel.imgpkg.bundle": "true"}})
}

// Push writes img to repo, tagged latest unless repo names a tag, over plain HTTP. It returns img's digest.
func Push(ctx context.Context, img v1.Image, repo string) (v1.Hash, error) {
	tag, err := name.NewTag(repo, name.Insecure)
	if err != nil {
		return v1.Hash{}, err
	}
	if err := remote.Write(tag, img, remote.WithContext(ctx)); err != nil {
		return v1.Hash{}, err
	}
	return img.Digest()
}

type file struct {
	path    string
	content []byte
	mode    int64
}

// layer packs files in path order with a fixed time, so equal files give equal digests.
func layer(files []file, mediaType types.MediaType) (v1.Layer, error) {
	slices.SortFunc(files, func(a, b file) int { return strings.Compare(a.path, b.path) })
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: f.path, Mode: f.mode, Size: int64(len(f.content)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil },
		tarball.WithMediaType(mediaType), tarball.WithCompressedCaching)
}
