package devenv

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
)

// capture checkpoints the VM to file once no MachineHealthCheck counts a machine unhealthy,
// so that restores resume no remediation. The VM runs slowly after a capture.
func (e *Environment) capture(ctx context.Context, file string) error {
	if err := e.stage("machines healthy", func() error { return e.wait(ctx, e.machinesHealthyGate()) }); err != nil {
		return err
	}
	if err := e.stage("prepare capture", func() error {
		filled, err := e.vm.PrepareCapture(ctx, hostAvailableMiB())
		if err == nil {
			e.progress(fmt.Sprintf("zero-filled %d MiB of guest memory", filled))
		}
		return err
	}); err != nil {
		return err
	}
	if err := e.stage("capture", func() error { return e.vm.Checkpoint(ctx, file) }); err != nil {
		return err
	}
	if info, err := os.Stat(file); err == nil {
		e.progress(fmt.Sprintf("checkpoint: %d MiB", info.Size()>>20))
	}
	return nil
}

func (e *Environment) machinesHealthyGate() ready.Gate {
	return ready.Gate{
		Name: "MachineHealthChecks count every machine healthy", Timeout: 3 * time.Minute, Interval: 2 * time.Second, Attempt: apiAttempt,
		Check: func(ctx context.Context) error {
			dyn, err := kube.Dynamic(e.MgmtKubeconfig)
			if err != nil {
				return err
			}
			return kube.MachinesHealthy(ctx, dyn, WorkloadNamespace)
		},
	}
}

// hostAvailableMiB returns the host's available memory, or 0 where it cannot tell.
func hostAvailableMiB() int {
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return memAvailableMiB(string(meminfo))
}

func memAvailableMiB(meminfo string) int {
	for _, line := range strings.Split(meminfo, "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kib, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			if err != nil {
				return 0
			}
			return kib >> 10
		}
	}
	return 0
}
