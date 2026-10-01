// Package ready waits for readiness gates with per-gate timeouts.
package ready

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// Log, if set, receives each failed check's error, and how long the gate took to pass.
	Log io.Writer
}

// Wait polls the gate until its check passes. If its timeout or ctx ends first, the error names the gate
// and the error of the last check that the timeout did not cut short.
func Wait(ctx context.Context, g Gate) error {
	start := time.Now()
	ctx, cancel := context.WithTimeoutCause(ctx, g.Timeout, errTimeout)
	defer cancel()
	var last error
	for {
		err := attempt(ctx, g)
		if err == nil {
			g.logf("gate %q met after %.1fs\n", g.Name, time.Since(start).Seconds())
			return nil
		}
		if ctx.Err() == nil || last == nil {
			last = err
			g.logf("gate %q: %v\n", g.Name, err)
		}
		select {
		case <-ctx.Done():
			if cause := context.Cause(ctx); !errors.Is(cause, errTimeout) {
				return fmt.Errorf("gate %q: %w; last check: %v", g.Name, cause, last)
			}
			return fmt.Errorf("gate %q not met within %v: %w", g.Name, g.Timeout, last)
		case <-time.After(g.Interval):
		}
	}
}

func (g Gate) logf(format string, args ...any) {
	if g.Log != nil {
		fmt.Fprintf(g.Log, format, args...)
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
