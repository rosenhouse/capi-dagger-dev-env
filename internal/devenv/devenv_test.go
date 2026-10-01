package devenv

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestStageReportsProgressAndNamesFailures(t *testing.T) {
	var progress bytes.Buffer
	e := &Environment{opts: Options{Progress: &progress}, start: time.Now()}

	if err := e.stage("docker daemon", func() error { return nil }); err != nil {
		t.Errorf("successful stage returned %v", err)
	}
	err := e.stage("management cluster", func() error { return errors.New("boom") })

	if err == nil || err.Error() != `stage "management cluster": boom` {
		t.Errorf("err = %v", err)
	}
	for _, name := range []string{"docker daemon", "management cluster"} {
		if !strings.Contains(progress.String(), name) {
			t.Errorf("progress %q does not mention %q", progress.String(), name)
		}
	}
}

// Every unpinned image a package's config references must be locked by that package, and vice versa.
func TestPackageImagesMatchConfigPlaceholders(t *testing.T) {
	for _, p := range packages {
		got := placeholders(t, filepath.Join("..", "..", "config", p.name))
		want := slices.Clone(p.images)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: config placeholders %v, package images %v", p.name, got, want)
		}
	}
}

func TestSubsetRejectsMissingKeys(t *testing.T) {
	if _, err := subset(map[string]string{"a": "1"}, []string{"a", "b"}); err == nil {
		t.Error("no error for missing key")
	}
}

// placeholders returns the sorted image values in dir that kbld must resolve: every "image" field,
// and every field named by a kbld searchRule, except digest-pinned references.
func placeholders(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no config in %s: %v", dir, err)
	}
	var docs []map[string]any
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			obj := map[string]any{}
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			docs = append(docs, obj)
		}
	}
	keys := map[string]bool{"image": true}
	for _, doc := range docs {
		if doc["kind"] == "Config" {
			rules, _ := doc["searchRules"].([]any)
			for _, r := range rules {
				name, _ := r.(map[string]any)["keyMatcher"].(map[string]any)["name"].(string)
				keys[name] = true
			}
		}
	}
	found := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if s, ok := child.(string); ok && keys[k] && !strings.Contains(s, "@sha256:") {
					found[s] = true
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	for _, doc := range docs {
		if doc["kind"] != "Config" {
			walk(doc)
		}
	}
	return slices.Sorted(maps.Keys(found))
}

func TestMissingMirroredReposNamesWhatEachMirrorLacks(t *testing.T) {
	want := map[string][]string{"docker.io": {"kindest/node", "kindest/kindnetd"}, "quay.io": {"jetstack/cert-manager-controller"}}
	catalogs := map[string]string{"docker.io": "kindest/kindnetd\nlibrary/nginx\n", "quay.io": "jetstack/cert-manager-controller\n"}

	err := missingMirroredRepos(catalogs, want)

	if err == nil || err.Error() != "mirrors lack repositories: docker.io/kindest/node" {
		t.Errorf("err = %v", err)
	}
	catalogs["docker.io"] += "kindest/node\n"
	if err := missingMirroredRepos(catalogs, want); err != nil {
		t.Errorf("err = %v", err)
	}
}
