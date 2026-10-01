package platform

import (
	"path"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestEveryDownloadIsChecksumPinned(t *testing.T) {
	sha256Hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, d := range Downloads() {
		if !sha256Hex.MatchString(d.SHA256) {
			t.Errorf("%s has checksum %q", d.URL, d.SHA256)
		}
	}
}

// clusterctl reads each provider's components, and the metadata.yaml beside them, from the guest.
func TestClusterctlConfigNamesFilesThatDownloadsPutInTheGuest(t *testing.T) {
	var cfg struct {
		Providers   []struct{ URL string }
		CertManager struct{ URL string } `json:"cert-manager"`
	}
	if err := yaml.Unmarshal([]byte(clusterctlConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	inGuest := map[string]bool{}
	for _, d := range Downloads() {
		inGuest[d.Path] = true
	}
	want := []string{cfg.CertManager.URL}
	for _, p := range cfg.Providers {
		want = append(want, p.URL, path.Join(path.Dir(p.URL), "metadata.yaml"))
	}
	if len(cfg.Providers) != 4 {
		t.Errorf("%d providers, want 4", len(cfg.Providers))
	}
	for _, file := range want {
		if !inGuest[file] {
			t.Errorf("no download puts %q in the guest", file)
		}
	}
}
