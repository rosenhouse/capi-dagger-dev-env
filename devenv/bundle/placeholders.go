package bundle

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// Placeholders returns the sorted image values in a package's config directory that kbld must resolve:
// every "image" field, and every field a kbld Config's searchRules name, except digest-pinned references.
// A package's images lock should name exactly these.
func Placeholders(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no YAML in %s", dir)
	}
	var docs []map[string]any
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			obj := map[string]any{}
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			docs = append(docs, obj)
		}
	}
	keys := map[string]bool{"image": true}
	for _, doc := range docs {
		if doc["kind"] == "Config" {
			rules, _ := doc["searchRules"].([]any)
			for _, r := range rules {
				rule, _ := r.(map[string]any)
				matcher, _ := rule["keyMatcher"].(map[string]any)
				if name, ok := matcher["name"].(string); ok {
					keys[name] = true
				}
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
	return slices.Sorted(maps.Keys(found)), nil
}
