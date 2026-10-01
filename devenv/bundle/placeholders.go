package bundle

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Placeholders returns the sorted image values in a package's config directory that kbld must resolve:
// every "image" field, and every field a kbld Config's searchRules name, except digest-pinned references.
// A package's images lock should name exactly these.
func Placeholders(dir string) ([]string, error) {
	var docs []map[string]any
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r := utilyaml.NewYAMLReader(bufio.NewReader(f))
		for {
			doc, err := r.Read()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			obj := map[string]any{}
			if err := yaml.Unmarshal(doc, &obj); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			docs = append(docs, obj)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no YAML in %s", dir)
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
