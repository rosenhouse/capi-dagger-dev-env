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
	Check    func(context.Context) error
}

// Wait polls the gate until its check passes. On timeout, the error names the stage, the gate, and the last check error.
func Wait(ctx context.Context, stage string, g Gate) error {
	ctx, cancel := context.WithTimeoutCause(ctx, g.Timeout, errTimeout)
	defer cancel()
	for {
		err := g.Check(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), errTimeout) {
				return fmt.Errorf("stage %q: gate %q not met within %v: %w", stage, g.Name, g.Timeout, err)
			}
			return ctx.Err()
		case <-time.After(g.Interval):
		}
	}
}

var errTimeout = errors.New("gate timeout")
