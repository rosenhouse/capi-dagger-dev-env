// Package fetch downloads files into a cache keyed by their sha256.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// File is a download pinned by the hex sha256 of its content.
type File struct{ URL, SHA256 string }

// Cache keeps downloads in Dir.
type Cache struct{ Dir string }

const attempts = 3

var (
	retryDelay = 2 * time.Second
	// attemptTimeout bounds each download, so a stalled one is retried.
	attemptTimeout = 10 * time.Minute
)

// Get returns the path of f's content in the cache. If the cache lacks it, Get downloads it first.
func (c Cache) Get(ctx context.Context, f File) (string, error) {
	path := filepath.Join(c.Dir, "sha256", f.SHA256)
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		if err = download(ctx, f, path); err == nil {
			return path, nil
		}
	}
	return "", err
}

// download writes f to path once its content matches, so a reader never sees a partial file.
func download(ctx context.Context, f File, path string) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", f.URL, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", f.URL, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.SHA256 {
		return fmt.Errorf("download %s: sha256 %s, want %s", f.URL, got, f.SHA256)
	}
	return os.Rename(tmp.Name(), path)
}
