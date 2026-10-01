package devenv

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestSubsetRejectsMissingKeys(t *testing.T) {
	if _, err := subset(map[string]string{"a": "1"}, []string{"a", "b"}); err == nil {
		t.Error("no error for missing key")
	}
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
