package fetch_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/fetch"
)

const content = "kind binary"

var contentSHA = sum(content)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// serve replies to every request with the given responses in turn, repeating the last.
func serve(t *testing.T, responses ...func(http.ResponseWriter)) (url string, requests *atomic.Int64) {
	t.Helper()
	requests = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requests.Add(1)
		responses[min(int(n), len(responses))-1](w)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/kind", requests
}

func body(s string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.Write([]byte(s)) }
}

func status(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

func TestGetDownloadsOnceAndThenReadsTheCache(t *testing.T) {
	url, requests := serve(t, body(content))
	cache := fetch.Cache{Dir: filepath.Join(t.TempDir(), "absent")}

	first, err := cache.Get(t.Context(), fetch.File{URL: url, SHA256: contentSHA})
	if err != nil {
		t.Fatal(err)
	}
	again, err := cache.Get(t.Context(), fetch.File{URL: url + "?moved", SHA256: contentSHA})
	if err != nil {
		t.Fatal(err)
	}

	if first != again {
		t.Errorf("paths %s and %s differ", first, again)
	}
	if got, err := os.ReadFile(first); err != nil || string(got) != content {
		t.Errorf("cached %q, %v", got, err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestGetRejectsContentThatDoesNotMatch(t *testing.T) {
	fetch.SetRetryDelay(t, 0)
	url, _ := serve(t, body("tampered"))
	cache := fetch.Cache{Dir: t.TempDir()}

	_, err := cache.Get(t.Context(), fetch.File{URL: url, SHA256: contentSHA})

	if err == nil || !strings.Contains(err.Error(), sum("tampered")) {
		t.Errorf("err = %v, want it to name the downloaded sha256", err)
	}
	if entries := cachedFiles(t, cache.Dir); len(entries) > 0 {
		t.Errorf("cache holds %v", entries)
	}
}

func TestGetRetriesAFailedDownload(t *testing.T) {
	fetch.SetRetryDelay(t, 0)
	url, requests := serve(t, status(http.StatusBadGateway), body("trunc"), body(content))

	path, err := fetch.Cache{Dir: t.TempDir()}.Get(t.Context(), fetch.File{URL: url, SHA256: contentSHA})

	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != content || requests.Load() != 3 {
		t.Errorf("cached %q after %d requests", got, requests.Load())
	}
}

func TestGetGivesUpWithTheLastError(t *testing.T) {
	fetch.SetRetryDelay(t, 0)
	url, requests := serve(t, status(http.StatusNotFound))

	_, err := fetch.Cache{Dir: t.TempDir()}.Get(t.Context(), fetch.File{URL: url, SHA256: contentSHA})

	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), url) {
		t.Errorf("err = %v, want the URL and status", err)
	}
	if requests.Load() < 2 {
		t.Errorf("%d requests, want retries", requests.Load())
	}
}

func TestConcurrentGetsShareOneEntry(t *testing.T) {
	url, _ := serve(t, body(content))
	cache := fetch.Cache{Dir: t.TempDir()}
	paths := make([]string, 8)
	var wg sync.WaitGroup
	for i := range paths {
		wg.Go(func() {
			var err error
			if paths[i], err = cache.Get(t.Context(), fetch.File{URL: url, SHA256: contentSHA}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	for _, p := range paths {
		if got, err := os.ReadFile(p); err != nil || string(got) != content {
			t.Errorf("%s holds %q, %v", p, got, err)
		}
	}
	if entries := cachedFiles(t, cache.Dir); len(entries) != 1 {
		t.Errorf("cache holds %v, want one file", entries)
	}
}

func cachedFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
