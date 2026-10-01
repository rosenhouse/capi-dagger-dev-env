package ready_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/devenv/ready"
)

func TestWaitReturnsOnceCheckPasses(t *testing.T) {
	calls := 0
	g := ready.Gate{Name: "nodes ready", Timeout: time.Second, Interval: time.Millisecond, Check: func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	}}

	if err := ready.Wait(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWaitTimeoutNamesGateAndLastError(t *testing.T) {
	g := ready.Gate{Name: "nodes ready", Timeout: 20 * time.Millisecond, Interval: time.Millisecond, Check: func(context.Context) error {
		return errors.New("node mgmt-control-plane NotReady")
	}}

	err := ready.Wait(context.Background(), g)

	for _, want := range []string{"nodes ready", "20ms", "node mgmt-control-plane NotReady"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

func TestWaitStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := ready.Gate{Name: "never", Timeout: time.Hour, Interval: time.Millisecond, Check: func(context.Context) error {
		return errors.New("not yet")
	}}

	if err := ready.Wait(ctx, g); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestWaitRetriesAnAttemptThatHangs(t *testing.T) {
	calls := 0
	g := ready.Gate{Name: "port-forward", Timeout: time.Second, Interval: time.Millisecond, Attempt: 20 * time.Millisecond,
		Check: func(ctx context.Context) error {
			calls++
			if calls == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}}

	if err := ready.Wait(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestWaitLeavesChecksUnboundedWithoutAttempt(t *testing.T) {
	g := ready.Gate{Name: "nodes ready", Timeout: time.Second, Interval: time.Millisecond, Check: func(ctx context.Context) error {
		return ctx.Err()
	}}

	if err := ready.Wait(context.Background(), g); err != nil {
		t.Error(err)
	}
}
