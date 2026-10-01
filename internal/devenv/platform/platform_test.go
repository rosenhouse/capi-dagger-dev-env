package platform

import "testing"

func TestEveryReleaseAssetIsChecksumPinned(t *testing.T) {
	for _, f := range append(clusterctlFiles, struct{ dir, file string }{"", "metadata.yaml"}) {
		if capiReleaseChecksums[f.file] == "" {
			t.Errorf("%s has no checksum", f.file)
		}
	}
}
