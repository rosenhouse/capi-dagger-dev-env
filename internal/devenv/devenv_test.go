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
