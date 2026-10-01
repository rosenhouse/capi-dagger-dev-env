package oci_test

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/oci"
)

var (
	amd64 = v1.Platform{OS: "linux", Architecture: "amd64"}
	arm64 = v1.Platform{OS: "linux", Architecture: "arm64"}
)

func TestBasePullsThePlatformsImageOnceAndCachesIt(t *testing.T) {
	srv, ref, want := multiPlatformBase(t)
	cache := t.TempDir()

	gotARM := base(t, ref, arm64, cache)
	gotAMD := base(t, ref, amd64, cache)
	srv.Close()
	again := base(t, ref, arm64, cache)

	for _, c := range []struct {
		img      v1.Image
		platform v1.Platform
	}{{gotARM, arm64}, {gotAMD, amd64}, {again, arm64}} {
		if got := digest(t, c.img); got != want[c.platform.String()] {
			t.Errorf("%s: digest %s, want %s", c.platform, got, want[c.platform.String()])
		}
		readLayers(t, c.img)
	}
}

func TestBaseSharesTheCacheBetweenConcurrentCalls(t *testing.T) {
	_, ref, want := multiPlatformBase(t)
	cache := t.TempDir()

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			img, err := oci.Base(context.Background(), ref, arm64, cache)
			if err != nil {
				errs[i] = err
				return
			}
			if d, err := img.Digest(); err != nil || d != want[arm64.String()] {
				errs[i] = fmt.Errorf("digest %s, %v; want %s", d, err, want[arm64.String()])
			}
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Error(err)
	}
}

func TestBaseRejectsAnImageForAnotherPlatform(t *testing.T) {
	_, ref, want := multiPlatformBase(t)
	repo, _, _ := strings.Cut(ref, "@")
	amd64Image := repo + "@" + want[amd64.String()].String()
	cache := t.TempDir()

	if _, err := oci.Base(context.Background(), amd64Image, amd64, cache); err != nil {
		t.Errorf("amd64 image for amd64: %v", err)
	}
	if _, err := oci.Base(context.Background(), amd64Image, arm64, cache); err == nil {
		t.Error("amd64 image for arm64: no error")
	}
}

func TestBaseRejectsACacheEntryWithSeveralImages(t *testing.T) {
	srv, ref, _ := multiPlatformBase(t)
	cache := t.TempDir()
	base(t, ref, arm64, cache)
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache holds %v, %v; want one entry", entries, err)
	}
	p, err := layout.FromPath(filepath.Join(cache, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendImage(empty.Image); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	_, err = oci.Base(context.Background(), ref, arm64, cache)

	if err == nil || !strings.Contains(err.Error(), entries[0].Name()) {
		t.Errorf("err = %v, want one naming the cache entry", err)
	}
}

func TestBaseRequiresADigest(t *testing.T) {
	_, ref, _ := multiPlatformBase(t)
	repo, _, _ := strings.Cut(ref, "@")

	if _, err := oci.Base(context.Background(), repo+":latest", arm64, t.TempDir()); err == nil {
		t.Error("no error for a tag")
	}
}

func TestImageAddsTheBinaryAsEntrypoint(t *testing.T) {
	baseImg := baseImage(t, types.OCIManifestSchema1, types.OCIConfigJSON, types.OCILayer)
	host := newRegistry(t).Listener.Addr().String()

	img, err := oci.Image(baseImg, "hello", []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := oci.Push(context.Background(), img, host+"/hello")
	if err != nil {
		t.Fatal(err)
	}

	got := pull(t, fmt.Sprintf("%s/hello@%s", host, d))
	cfg, err := got.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Config.Entrypoint, []string{"/hello"}) || cfg.Config.Cmd != nil || cfg.Config.User != "65532" {
		t.Errorf("entrypoint %q, cmd %q, user %q; want [/hello], none, the base's 65532", cfg.Config.Entrypoint, cfg.Config.Cmd, cfg.Config.User)
	}
	layers, err := got.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(layers); n != 2 {
		t.Fatalf("%d layers, want the base's and one more", n)
	}
	want := map[string]tarFile{"hello": {0o755, "binary"}}
	if files := tarFiles(t, layers[1]); !maps.Equal(files, want) {
		t.Errorf("layer holds %v, want %v", files, want)
	}
}

func TestImageLayerMatchesTheBasesManifestType(t *testing.T) {
	for _, c := range []struct {
		manifest, config, layer types.MediaType
	}{
		{types.OCIManifestSchema1, types.OCIConfigJSON, types.OCILayer},
		{types.DockerManifestSchema2, types.DockerConfigJSON, types.DockerLayer},
	} {
		img, err := oci.Image(baseImage(t, c.manifest, c.config, c.layer), "hello", []byte("binary"))
		if err != nil {
			t.Fatal(err)
		}
		layers, err := img.Layers()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := layers[len(layers)-1].MediaType(); err != nil || got != c.layer {
			t.Errorf("on a %s base: layer %s, %v; want %s", c.manifest, got, err, c.layer)
		}
	}
}

func TestImageIsReproducible(t *testing.T) {
	baseImg := baseImage(t, types.OCIManifestSchema1, types.OCIConfigJSON, types.OCILayer)
	build := func() v1.Hash {
		img, err := oci.Image(baseImg, "hello", []byte("binary"))
		if err != nil {
			t.Fatal(err)
		}
		return digest(t, img)
	}

	if first, second := build(), build(); first != second {
		t.Errorf("digests %s and %s", first, second)
	}
}

func TestBundleIsAnImgpkgBundle(t *testing.T) {
	host := newRegistry(t).Listener.Addr().String()
	config := map[string][]byte{"a.yaml": []byte("a: 1\n"), "sub/b.yaml": []byte("b: 2\n")}

	img, err := oci.Bundle(config, []byte("lock\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := oci.Push(context.Background(), img, host+"/bundles/hello")
	if err != nil {
		t.Fatal(err)
	}

	got := pull(t, fmt.Sprintf("%s/bundles/hello@%s", host, d))
	cfg, err := got.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if label := cfg.Config.Labels["dev.carvel.imgpkg.bundle"]; label != "true" {
		t.Errorf("bundle label %q", label)
	}
	layers, err := got.Layers()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]tarFile{
		".imgpkg/images.yml": {0o644, "lock\n"},
		"config/a.yaml":      {0o644, "a: 1\n"},
		"config/sub/b.yaml":  {0o644, "b: 2\n"},
	}
	if len(layers) != 1 {
		t.Fatalf("%d layers, want 1", len(layers))
	}
	if files := tarFiles(t, layers[0]); !maps.Equal(files, want) {
		t.Errorf("bundle holds %v, want %v", files, want)
	}
}

func TestBundleIsReproducible(t *testing.T) {
	config := map[string][]byte{}
	for i := range 20 {
		config[fmt.Sprintf("f%d.yaml", i)] = fmt.Appendf(nil, "i: %d\n", i)
	}
	digests := map[v1.Hash]bool{}

	for range 5 {
		img, err := oci.Bundle(config, []byte("lock\n"))
		if err != nil {
			t.Fatal(err)
		}
		digests[digest(t, img)] = true
	}

	if len(digests) != 1 {
		t.Errorf("digests %v", slices.Collect(maps.Keys(digests)))
	}
}

func TestPushTagsLatestAndReturnsTheDigest(t *testing.T) {
	host := newRegistry(t).Listener.Addr().String()
	img := baseImage(t, types.OCIManifestSchema1, types.OCIConfigJSON, types.OCILayer)

	d, err := oci.Push(context.Background(), img, host+"/hello")
	if err != nil {
		t.Fatal(err)
	}

	if got := digest(t, pull(t, host+"/hello:latest")); got != d || d != digest(t, img) {
		t.Errorf("Push returned %s; latest is %s; the image is %s", d, got, digest(t, img))
	}
}

func TestPushUsesPlainHTTPForAnyHost(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("needs 127.0.0.2: %v", err)
	}
	srv := httptest.NewUnstartedServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	img := baseImage(t, types.OCIManifestSchema1, types.OCIConfigJSON, types.OCILayer)

	if _, err := oci.Push(context.Background(), img, l.Addr().String()+"/hello"); err != nil {
		t.Error(err)
	}
}

// multiPlatformBase serves an index of two random images and returns its digest reference.
func multiPlatformBase(t *testing.T) (*httptest.Server, string, map[string]v1.Hash) {
	t.Helper()
	srv := newRegistry(t)
	want := map[string]v1.Hash{}
	var idx v1.ImageIndex = empty.Index
	for _, p := range []v1.Platform{amd64, arm64} {
		img, err := random.Image(1024, 2)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cfg.OS, cfg.Architecture = p.OS, p.Architecture
		if img, err = mutate.ConfigFile(img, cfg); err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &p}})
		want[p.String()] = digest(t, img)
	}
	repo := srv.Listener.Addr().String() + "/base"
	tag, err := name.NewTag(repo + ":latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatal(err)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return srv, repo + "@" + d.String(), want
}

// baseImage is a one-layer image with the given media types that runs as user 65532 with a default command.
func baseImage(t *testing.T, manifest, config, layer types.MediaType) v1.Image {
	t.Helper()
	l, err := random.Layer(1024, layer)
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(mutate.ConfigMediaType(mutate.MediaType(empty.Image, manifest), config), l)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.Config(img, v1.Config{User: "65532", Cmd: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func newRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	return srv
}

func base(t *testing.T, ref string, platform v1.Platform, cache string) v1.Image {
	t.Helper()
	img, err := oci.Base(context.Background(), ref, platform, cache)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func pull(t *testing.T, ref string) v1.Image {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	img, err := remote.Image(r)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func digest(t *testing.T, img v1.Image) v1.Hash {
	t.Helper()
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readLayers(t *testing.T, img v1.Image) {
	t.Helper()
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

type tarFile struct {
	mode    int64
	content string
}

func tarFiles(t *testing.T, l v1.Layer) map[string]tarFile {
	t.Helper()
	rc, err := l.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	files := map[string]tarFile{}
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if !h.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("%s has time %v, want the epoch", h.Name, h.ModTime)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = tarFile{h.Mode, string(b)}
	}
}
