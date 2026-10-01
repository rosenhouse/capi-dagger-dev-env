// Package ready waits for readiness gates with per-gate timeouts.
package ready

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Gate is one readiness condition. Check returns nil when the condition holds.
type Gate struct {
	Name     string
	Timeout  time.Duration
	Interval time.Duration
	// Attempt, if set, bounds each check, so a check that hangs is retried.
	Attempt time.Duration
	Check   func(context.Context) error
}

// Wait polls the gate until its check passes. On timeout, the error names the gate and the last check error.
func Wait(ctx context.Context, g Gate) error {
	ctx, cancel := context.WithTimeoutCause(ctx, g.Timeout, errTimeout)
	defer cancel()
	for {
		err := attempt(ctx, g)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), errTimeout) {
				return fmt.Errorf("gate %q not met within %v: %w", g.Name, g.Timeout, err)
			}
			return ctx.Err()
		case <-time.After(g.Interval):
		}
	}
}

var errTimeout = errors.New("gate timeout")

func attempt(ctx context.Context, g Gate) error {
	if g.Attempt > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Attempt)
		defer cancel()
	}
	return g.Check(ctx)
}
