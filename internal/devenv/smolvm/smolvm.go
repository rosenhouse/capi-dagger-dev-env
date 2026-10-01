// Package smolvm drives the smolvm CLI, which runs microVMs.
package smolvm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
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
	// Command holds smolvm's arguments, up to any "--".
	Command string
	// Code is smolvm's exit code. Exec passes on the guest command's code, but smolvm's own failures exit 1 too.
	Code int
	// Stderr holds the last lines of stderr.
	Stderr string
}

func (e *ExitError) Error() string {
	return strings.TrimSuffix(fmt.Sprintf("smolvm %s: exit %d\n%s", e.Command, e.Code, e.Stderr), "\n")
}

// CheckVersion fails unless smolvm is installed at Version.
func (c CLI) CheckVersion(ctx context.Context) error {
	out, err := c.output(ctx, "--version")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, installHint)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != "smolvm" {
		return fmt.Errorf("smolvm --version printed %q\n%s", out, installHint)
	}
	if fields[1] != Version {
		return fmt.Errorf("smolvm is %s; want %s\n%s", fields[1], Version, installHint)
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
	var tail tail
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &tail
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &tail)
	}
	err := cmd.Run()
	command := args
	if i := slices.Index(args, "--"); i >= 0 {
		command = args[:i]
	}
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return &ExitError{Command: strings.Join(command, " "), Code: exit.ExitCode(), Stderr: tail.String()}
	}
	if err != nil {
		return fmt.Errorf("smolvm %s: %w", strings.Join(command, " "), err)
	}
	return nil
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
