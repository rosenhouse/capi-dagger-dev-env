package smolvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
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
// Once smolvm has begun, it lets it finish and only then returns ctx's error,
// because smolvm interrupted leaves its partial copy of the checkpoint where Delete cannot see it.
func (c CLI) CreateFromCheckpoint(ctx context.Context, name, file string) error {
	return c.finish(ctx, "machine", "create", "--name", name, "--from", file)
}

// RebindPorts removes and adds published ports of a machine that is not running.
// smolvm ignores a removal that matches no published port.
func (c CLI) RebindPorts(ctx context.Context, name string, remove, add []Port) error {
	args := withPorts([]string{"machine", "update", "--name", name}, "--remove-port", remove)
	return c.do(ctx, withPorts(args, "-p", add)...)
}

type StartOptions struct {
	// Branchable lets the machine be checkpointed. A machine created from a checkpoint is branchable without it.
	Branchable bool
	// NoIdleReclaim stops smolvm from squeezing the guest's memory to a fifth for a moment after ten idle minutes.
	NoIdleReclaim bool
}

// Start boots a machine, or resumes a machine created from a checkpoint.
// Once smolvm has begun, Start lets it finish and only then returns ctx's error,
// because a VM whose start smolvm was interrupted in runs on where Delete cannot see it.
func (c CLI) Start(ctx context.Context, name string, opts StartOptions) error {
	// A bare VM pulls no image. Without --proxy, smolvm fails if the host's proxy listens only on loopback.
	args := []string{"machine", "start", "--name", name, "--proxy", ""}
	if opts.Branchable {
		args = append(args, "--branchable")
	}
	if opts.NoIdleReclaim {
		c.env = append(slices.Clip(c.env), "SMOLVM_IDLE_RECLAIM=off")
	}
	return c.finish(ctx, args...)
}

// finish runs smolvm unless ctx has ended. It lets smolvm finish even if ctx ends meanwhile, then returns ctx's error.
func (c CLI) finish(ctx context.Context, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := c.do(context.WithoutCancel(ctx), args...)
	if ctx.Err() != nil {
		return fmt.Errorf("smolvm %s: %w", commandLine(args), ctx.Err())
	}
	return err
}

// AgentTimedOut reports whether a start failed because a restored machine's agent did not answer within smolvm's fixed 30 s.
func AgentTimedOut(err error) bool {
	var exit *ExitError
	return errors.As(err, &exit) && strings.Contains(exit.Stderr, "agent did not respond to ping within timeout")
}

func (c CLI) Stop(ctx context.Context, name string) error {
	return c.do(ctx, "machine", "stop", "--name", name)
}

func (c CLI) Delete(ctx context.Context, name string) error {
	return c.do(ctx, "machine", "delete", "--name", name, "-f")
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
)

type Machine struct {
	Name  string `json:"name"`
	State State  `json:"state"`
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
	// A positive Timeout makes smolvm kill the command and exit 124.
	Timeout time.Duration
}

// Exec runs argv in a running machine. A nonzero exit returns an *ExitError.
func (c CLI) Exec(ctx context.Context, name string, argv []string, opts ExecOptions) error {
	args := execArgs(name, opts.Env)
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%dms", (opts.Timeout+time.Millisecond-1)/time.Millisecond))
	}
	if opts.Stdin != nil {
		args = append(args, "-i")
	} else {
		args = append(args, "--stream")
	}
	return c.run(ctx, append(append(args, "--"), argv...), opts.Stdin, opts.Stdout, opts.Stderr)
}

// Spawn starts argv, such as a daemon, in the background in a running machine and returns its guest PID.
// env holds KEY=VALUE pairs.
func (c CLI) Spawn(ctx context.Context, name string, argv, env []string) (int, error) {
	args := append(append(execArgs(name, env), "-d", "--"), argv...)
	out, err := c.output(ctx, args...)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("smolvm %s printed %q; want a PID", commandLine(args), out)
	}
	return pid, nil
}

func execArgs(name string, env []string) []string {
	args := []string{"machine", "exec", "--name", name}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	return args
}

// Run runs script with "sh -euo pipefail -c" in a running machine and returns its stdout,
// which opts.Stdout also receives if set. Its errors start with the script's first line.
func (c CLI) Run(ctx context.Context, name, script string, opts ExecOptions) (string, error) {
	var stdout strings.Builder
	if opts.Stdout != nil {
		opts.Stdout = io.MultiWriter(&stdout, opts.Stdout)
	} else {
		opts.Stdout = &stdout
	}
	if err := c.Exec(ctx, name, []string{"sh", "-euo", "pipefail", "-c", script}, opts); err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(script), "\n")
		return "", fmt.Errorf("%s: %w", first, err)
	}
	return stdout.String(), nil
}

// CopyIn copies the host file src to dst in a running machine, creating dst's parents.
func (c CLI) CopyIn(ctx context.Context, name, src, dst string, mode fs.FileMode) error {
	if strings.Contains(src, ":") {
		return fmt.Errorf("smolvm cannot copy %s: its path has a colon", src)
	}
	return c.do(ctx, "machine", "cp", "--mode", strconv.FormatUint(uint64(mode.Perm()), 8), src, name+":"+dst)
}

// CopyOut copies the file src in a running machine to dst on the host.
func (c CLI) CopyOut(ctx context.Context, name, src, dst string) error {
	if strings.Contains(dst, ":") {
		return fmt.Errorf("smolvm cannot copy to %s: its path has a colon", dst)
	}
	return c.do(ctx, "machine", "cp", name+":"+src, dst)
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
