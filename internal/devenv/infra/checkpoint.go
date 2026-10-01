package infra

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Restore creates the VM from a checkpoint file. It publishes the checkpoint's host ports until smolvm.CLI.RebindPorts.
func (v *VM) Restore(ctx context.Context, checkpoint string) error {
	return v.CLI.CreateFromCheckpoint(ctx, v.Name, checkpoint)
}

// A capture skips zero pages, so zero-filling free guest memory keeps stale page cache out of the checkpoint.
// Each step can take as much host memory as it fills, so the steps stop short of both reserves.
// A VM restored from a guest filled to within 512 MiB of its memory stopped right after its start.
const (
	zeroFillStepMiB = 512
	hostReserveMiB  = 2048
	guestReserveMiB = 2048
)

func zeroFillSteps(hostMiB, guestMiB int) int {
	return max(0, min((hostMiB-hostReserveMiB)/zeroFillStepMiB, (guestMiB-guestReserveMiB)/zeroFillStepMiB))
}

// PrepareCapture drops the guest's page cache, zero-fills free guest memory while the host keeps hostMiB minus
// a reserve available, and trims and syncs the guest's disks. It returns how many MiB it zero-filled.
func (v *VM) PrepareCapture(ctx context.Context, hostMiB int) (int, error) {
	out, err := v.Output(ctx, `sync
echo 3 >/proc/sys/vm/drop_caches
awk '/^MemAvailable:/ { print int($2 / 1024) }' /proc/meminfo`)
	if err != nil {
		return 0, err
	}
	guestMiB, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("guest MemAvailable %q: %w", out, err)
	}
	steps := zeroFillSteps(hostMiB, guestMiB)
	// The capture syncs again, and gives up if that takes over 30 s.
	return steps * zeroFillStepMiB, v.Run(ctx, fmt.Sprintf(`mkdir -p /mnt/zero
mount -t tmpfs -o size=100%% zero /mnt/zero
i=0
while [ $i -lt %d ]; do dd if=/dev/zero of=/mnt/zero/$i bs=1M count=%d 2>/dev/null; i=$((i + 1)); done
rm -f /mnt/zero/*
umount /mnt/zero
fstrim /storage
fstrim / 2>/dev/null || true
sync`, steps, zeroFillStepMiB))
}
