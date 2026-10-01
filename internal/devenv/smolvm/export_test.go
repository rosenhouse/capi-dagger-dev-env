package smolvm

import (
	"testing"
	"time"
)

var LinuxAMD64Contract = linuxAMD64Contract

func SetWaitDelay(t *testing.T, d time.Duration) {
	old := waitDelay
	waitDelay = d
	t.Cleanup(func() { waitDelay = old })
}
