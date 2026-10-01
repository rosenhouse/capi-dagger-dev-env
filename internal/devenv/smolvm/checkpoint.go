package smolvm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// UnknownContract is HostContract's result where this package cannot compute it.
// smolvm still refuses to restore a checkpoint on an incompatible CPU.
const UnknownContract = "unknown"

// HostContract returns the CPU contract that smolvm records in checkpoints taken on this host,
// in CheckpointContract's form.
func HostContract() (string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return UnknownContract, nil
	}
	cpuinfo, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	return linuxAMD64Contract(string(cpuinfo)), nil
}

// linuxAMD64Contract mirrors checkpoint_cpu_contract and cpu_fingerprint in smolvm's src/portable_checkpoint.rs.
func linuxAMD64Contract(cpuinfo string) string {
	identity := "platform=linux/amd64\n"
	for _, line := range strings.Split(cpuinfo, "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		key, value, _ := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.Join(strings.Fields(value), " ")
		switch {
		case key == "vendor_id" && value == "GenuineIntel":
			return "linux-kvm-intel-portable-v1"
		case key == "vendor_id", key == "cpu family", key == "model", key == "stepping", key == "flags":
			identity += key + "=" + value + "\n"
		}
	}
	sum := sha256.Sum256([]byte(identity))
	return "exact-v1-" + hex.EncodeToString(sum[:])
}

// CheckpointContract returns the CPU contract recorded in a checkpoint file:
// "linux-kvm-intel-portable-v1", "exact-v1-" and a fingerprint, or the name of another kind.
func CheckpointContract(file string) (string, error) {
	manifest, err := readManifest(file)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	var m struct {
		Checkpoint struct {
			CPUContract struct{ Kind, Fingerprint string } `json:"cpu_contract"`
		}
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	contract := m.Checkpoint.CPUContract
	if contract.Kind == "" {
		return "", fmt.Errorf("%s: manifest has no checkpoint CPU contract", file)
	}
	if contract.Fingerprint != "" {
		return contract.Kind + "-" + contract.Fingerprint, nil
	}
	return contract.Kind, nil
}

// readManifest reads a checkpoint's manifest, which sits just before a 64-byte footer that gives its size.
// It does not verify the file; smolvm does when it restores the checkpoint.
// crates/smolvm-checkpoint/FORMAT.md §3 in smolvm's source describes the layout.
func readManifest(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	footer := make([]byte, 64)
	end := info.Size() - int64(len(footer))
	if end < 0 {
		return nil, errors.New("too short for a checkpoint")
	}
	if _, err := f.ReadAt(footer, end); err != nil {
		return nil, err
	}
	if string(footer[:8]) != "SMOLPACK" {
		return nil, errors.New("not a checkpoint")
	}
	size := binary.LittleEndian.Uint64(footer[44:])
	if size > min(16<<20, uint64(end)) {
		return nil, fmt.Errorf("manifest size %d exceeds 16 MiB or the file", size)
	}
	manifest := make([]byte, size)
	_, err = f.ReadAt(manifest, end-int64(size))
	return manifest, err
}
