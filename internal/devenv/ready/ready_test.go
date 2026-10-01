package ready_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
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

func TestWaitTimeoutReportsTheLastAttemptThatFinished(t *testing.T) {
	calls := 0
	g := ready.Gate{Name: "manifests applied", Timeout: 50 * time.Millisecond, Interval: time.Millisecond, Check: func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("webhook refused the connection")
		}
		<-ctx.Done()
		return ctx.Err()
	}}

	err := ready.Wait(context.Background(), g)

	if err == nil || !strings.Contains(err.Error(), "webhook refused the connection") {
		t.Errorf("err = %v", err)
	}
}

func TestWaitLogsFailedChecksAndHowLongTheGateTook(t *testing.T) {
	var log strings.Builder
	calls := 0
	g := ready.Gate{Name: "nodes ready", Timeout: time.Second, Interval: time.Millisecond, Log: &log, Check: func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("node a NotReady")
		}
		return nil
	}}

	if err := ready.Wait(context.Background(), g); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 || lines[0] != `gate "nodes ready": node a NotReady` || !strings.HasPrefix(lines[1], `gate "nodes ready" met after `) {
		t.Errorf("log = %q", lines)
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

func TestWaitNamesTheGateAndItsLastCheckWhenItsContextEnds(t *testing.T) {
	bound := errors.New("the restored platform took over 20ms")
	ctx, cancel := context.WithTimeoutCause(context.Background(), 20*time.Millisecond, bound)
	defer cancel()
	g := ready.Gate{Name: "leases renewed", Timeout: time.Hour, Interval: time.Millisecond, Check: func(context.Context) error {
		return errors.New("kube-system/kube-scheduler not renewed")
	}}

	err := ready.Wait(ctx, g)

	if want := `gate "leases renewed": the restored platform took over 20ms; last check: kube-system/kube-scheduler not renewed`; !errors.Is(err, bound) || err.Error() != want {
		t.Errorf("err = %v; want %s", err, want)
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
