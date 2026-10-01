package fetch

import (
	"testing"
	"time"
)

func SetRetryDelay(t *testing.T, d time.Duration) {
	old := retryDelay
	retryDelay = d
	t.Cleanup(func() { retryDelay = old })
}
