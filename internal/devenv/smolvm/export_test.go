package smolvm

import (
	"fmt"
	"testing"
	"time"
)

var LinuxAMD64Contract = linuxAMD64Contract

func SetWaitDelay(t *testing.T, d time.Duration) {
	old := waitDelay
	waitDelay = d
	t.Cleanup(func() { waitDelay = old })
}

// CheckpointContract returns the CPU contract recorded in a checkpoint file:
// "linux-kvm-intel-portable-v1", "exact-v1-" and a fingerprint, or the name of another kind.
func CheckpointContract(file string) (string, error) {
	c, err := readCheckpoint(file)
	if err != nil {
		return "", err
	}
	contract := c.CPUContract
	if contract.Kind == "" {
		return "", fmt.Errorf("%s: manifest has no checkpoint CPU contract", file)
	}
	if contract.Fingerprint != "" {
		return contract.Kind + "-" + contract.Fingerprint, nil
	}
	return contract.Kind, nil
}
