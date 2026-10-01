package kube

import (
	"testing"
	"time"
)

func SetRequestTimeout(t *testing.T, d time.Duration) {
	old := requestTimeout
	requestTimeout = d
	t.Cleanup(func() { requestTimeout = old })
}
