package devenv

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/control"
)

func TestRedeployWaitsForBringUp(t *testing.T) {
	e := started(t)

	err := control.Request(context.Background(), e.SocketPath(), "redeploy", io.Discard)

	if err == nil || !strings.Contains(err.Error(), "still coming up") {
		t.Errorf("err = %v", err)
	}
}

func TestRedeployRefusesToOverlapAnother(t *testing.T) {
	e := started(t)
	e.isUp.Store(true)
	e.redeploying.Lock()
	defer e.redeploying.Unlock()

	err := control.Request(context.Background(), e.SocketPath(), "redeploy", io.Discard)

	if err == nil || !strings.Contains(err.Error(), "another redeploy is in progress") {
		t.Errorf("err = %v", err)
	}
}

func TestRedeployRejectsVersionsThatLdflagsCannotTake(t *testing.T) {
	e := started(t)
	e.isUp.Store(true)
	for _, command := range []string{"redeploy it's", "redeploy a b"} {
		err := control.Request(context.Background(), e.SocketPath(), command, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("%q: err = %v", command, err)
		}
	}
}

func started(t *testing.T) *Environment {
	t.Helper()
	e, err := start(context.Background(), Options{StateDir: t.TempDir(), Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
