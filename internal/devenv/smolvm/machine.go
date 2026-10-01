package smolvm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Port publishes a guest port on a host port. smolvm binds it on 127.0.0.1 and ::1.
type Port struct{ Host, Guest int }

func (p Port) String() string { return fmt.Sprintf("%d:%d", p.Host, p.Guest) }

// MachineConfig sizes a bare VM. Zero values take smolvm's defaults.
type MachineConfig struct {
	CPUs, MemoryMiB, StorageGiB, OverlayGiB int
	Ports                                   []Port
}

// Create creates a bare VM with outbound network over virtio-net.
func (c CLI) Create(ctx context.Context, name string, cfg MachineConfig) error {
	args := []string{"machine", "create", "--name", name, "--net", "--net-backend", "virtio-net"}
	for _, flag := range []struct {
		name  string
		value int
	}{{"--cpus", cfg.CPUs}, {"--mem", cfg.MemoryMiB}, {"--storage", cfg.StorageGiB}, {"--overlay", cfg.OverlayGiB}} {
		if flag.value != 0 {
			args = append(args, flag.name, strconv.Itoa(flag.value))
		}
	}
	return c.do(ctx, withPorts(args, "-p", cfg.Ports)...)
}

// CreateFromCheckpoint creates a machine whose first start resumes the checkpoint file.
// The machine keeps the checkpoint's host ports; RebindPorts changes them.
func (c CLI) CreateFromCheckpoint(ctx context.Context, name, file string) error {
	return c.do(ctx, "machine", "create", "--name", name, "--from", file)
}

// RebindPorts replaces published ports of a machine that is not running.
func (c CLI) RebindPorts(ctx context.Context, name string, remove, add []Port) error {
	args := withPorts([]string{"machine", "update", "--name", name}, "--remove-port", remove)
	return c.do(ctx, withPorts(args, "-p", add)...)
}

type StartOptions struct {
	// Branchable lets the machine be branched and checkpointed.
	Branchable bool
}

// Start boots a machine, or resumes a machine created from a checkpoint.
func (c CLI) Start(ctx context.Context, name string, opts StartOptions) error {
	args := []string{"machine", "start", "--name", name}
	if opts.Branchable {
		args = append(args, "--branchable")
	}
	return c.do(ctx, args...)
}

func (c CLI) Stop(ctx context.Context, name string) error {
	return c.do(ctx, "machine", "stop", "--name", name)
}

type DeleteOptions struct {
	// Cascade also deletes the machine's branches.
	Cascade bool
}

func (c CLI) Delete(ctx context.Context, name string, opts DeleteOptions) error {
	args := []string{"machine", "delete", "--name", name, "-f"}
	if opts.Cascade {
		args = append(args, "--cascade")
	}
	return c.do(ctx, args...)
}

type BranchOptions struct {
	// FreezeSource pauses the source for good, as a base for more branches.
	FreezeSource bool
	// Ports publishes the branch's guest ports. Without them, smolvm picks random host ports for the source's.
	Ports []Port
}

// Branch creates and starts a copy-on-write copy of a running, branchable machine.
func (c CLI) Branch(ctx context.Context, from, name string, opts BranchOptions) error {
	args := []string{"machine", "branch", "--from", from, "--name", name}
	if opts.FreezeSource {
		args = append(args, "--freeze-source")
	}
	return c.do(ctx, withPorts(args, "-p", opts.Ports)...)
}

// Checkpoint saves a running, branchable machine, including its RAM, to file. The file name must end in ".checkpoint".
func (c CLI) Checkpoint(ctx context.Context, name, file string) error {
	return c.do(ctx, "machine", "checkpoint", "--name", name, "-o", file)
}

type State string

const (
	Created State = "created"
	Running State = "running"
	Stopped State = "stopped"
	// Frozen is the state of a source that FreezeSource paused.
	Frozen State = "frozen"
)

type Machine struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	// Parent names the machine this one branched from.
	Parent string `json:"parent_machine"`
}

func (c CLI) List(ctx context.Context) ([]Machine, error) {
	var machines []Machine
	return machines, c.decode(ctx, &machines, "machine", "ls", "--json")
}

func (c CLI) Status(ctx context.Context, name string) (Machine, error) {
	var m Machine
	return m, c.decode(ctx, &m, "machine", "status", "--name", name, "--json")
}

// DataDir returns the directory that holds the machine's disks and logs, such as agent-console.log.
func (c CLI) DataDir(ctx context.Context, name string) (string, error) {
	out, err := c.output(ctx, "machine", "data-dir", "--name", name)
	return strings.TrimSpace(out), err
}

type ExecOptions struct {
	// Env holds KEY=VALUE pairs.
	Env            []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Timeout makes smolvm kill the command and exit 124.
	Timeout time.Duration
	// Detach leaves the command running in the background. smolvm prints its guest PID.
	Detach bool
}

// Exec runs argv in a running machine. A nonzero exit returns an *ExitError.
func (c CLI) Exec(ctx context.Context, name string, argv []string, opts ExecOptions) error {
	args := []string{"machine", "exec", "--name", name}
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%dms", opts.Timeout.Milliseconds()))
	}
	switch {
	case opts.Detach:
		args = append(args, "-d")
	case opts.Stdin != nil:
		args = append(args, "-i")
	default:
		args = append(args, "--stream")
	}
	return c.run(ctx, append(append(args, "--"), argv...), opts.Stdin, opts.Stdout, opts.Stderr)
}

// Run runs script with "sh -euc" in a running machine and returns its stdout.
// Its errors start with the script's first line.
func (c CLI) Run(ctx context.Context, name, script string) (string, error) {
	var stdout strings.Builder
	if err := c.Exec(ctx, name, []string{"sh", "-euc", script}, ExecOptions{Stdout: &stdout}); err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(script), "\n")
		return "", fmt.Errorf("%s: %w", first, err)
	}
	return stdout.String(), nil
}

func (c CLI) do(ctx context.Context, args ...string) error {
	return c.run(ctx, args, nil, nil, nil)
}

func (c CLI) decode(ctx context.Context, v any, args ...string) error {
	out, err := c.output(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("smolvm %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func withPorts(args []string, flag string, ports []Port) []string {
	for _, p := range ports {
		args = append(args, flag, p.String())
	}
	return args
}
