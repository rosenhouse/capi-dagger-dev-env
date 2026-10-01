// Package smolvm drives the smolvm CLI, which runs microVMs.
package smolvm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Version is the smolvm release this package drives.
const Version = "1.22.0"

const installHint = "Install smolvm " + Version + " with: curl -fsSL https://raw.githubusercontent.com/smol-machines/smolvm/v" +
	Version + "/scripts/install.sh | bash -s -- --version " + Version

// CLI runs smolvm commands. The zero value runs smolvm from PATH.
type CLI struct {
	// Path is the smolvm executable.
	Path string
}

// ExitError reports a smolvm command that exited nonzero.
type ExitError struct {
	// Command holds smolvm's arguments without environment values. For exec, it ends with the guest command's name.
	Command string
	// Code is smolvm's exit code. Exec passes on the guest command's code, but smolvm's own failures exit 1 too.
	Code int
	// Stderr holds the last lines of stderr.
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("smolvm %s: exit %d%s", e.Command, e.Code, onNewLine(e.Stderr))
}

// CheckVersion fails unless smolvm is installed at Version.
func (c CLI) CheckVersion(ctx context.Context) error {
	out, err := c.output(ctx, "--version")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, installHint)
	}
	if strings.TrimSpace(out) != "smolvm "+Version {
		return fmt.Errorf("smolvm --version printed %q; want smolvm %s\n%s", out, Version, installHint)
	}
	return nil
}

func (c CLI) output(ctx context.Context, args ...string) (string, error) {
	var out strings.Builder
	err := c.run(ctx, args, nil, &out, nil)
	return out.String(), err
}

func (c CLI) run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, cmp.Or(c.Path, "smolvm"), args...)
	// SIGINT lets smolvm kill the VM once its agent is up. Before that, either signal leaves the VM running.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = waitDelay
	var tail tail
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &tail
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &tail)
	}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	command := commandLine(args)
	if ctx.Err() != nil {
		return fmt.Errorf("smolvm %s: %w%s", command, ctx.Err(), onNewLine(tail.String()))
	}
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return &ExitError{Command: command, Code: exit.ExitCode(), Stderr: tail.String()}
	}
	return fmt.Errorf("smolvm %s: %w", command, err)
}

func onNewLine(s string) string {
	if s == "" {
		return ""
	}
	return "\n" + s
}

// waitDelay bounds how long smolvm may ignore SIGINT.
var waitDelay = 5 * time.Second

// commandLine returns args up to the one after any "--", without environment values.
func commandLine(args []string) string {
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[:min(i+2, len(args))]
	}
	args = slices.Clone(args)
	for i := 1; i < len(args); i++ {
		if args[i-1] == "-e" {
			args[i], _, _ = strings.Cut(args[i], "=")
		}
	}
	return strings.Join(args, " ")
}

const (
	tailBytes = 4096
	tailLines = 20
)

// tail keeps the end of what is written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	t.b = t.b[max(0, len(t.b)-tailBytes):]
	return len(p), nil
}

func (t *tail) String() string {
	lines := strings.Split(strings.TrimRight(string(t.b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-tailLines):], "\n")
}
