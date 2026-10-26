package policy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// Executor performs the steps that affect the machine. The engine never
// runs commands itself, so tests can substitute a recording fake.
type Executor interface {
	// RunHook runs a pre-shutdown hook through the shell.
	RunHook(ctx context.Context, command string, env []string) error
	// Shutdown shuts the machine down: with command, through the shell;
	// with "", using the operating system's own shutdown command.
	Shutdown(ctx context.Context, command string) error
}

// ErrRefused means the built-in OS shutdown was blocked by a safety guard.
var ErrRefused = errors.New("refused to shut down")

// NeverShutdownEnv, when set to any value, blocks the built-in OS shutdown
// command. Use it when testing a shutdown policy on a machine that must
// stay up; custom shutdown.command values still run.
const NeverShutdownEnv = "NUTNG_NEVER_SHUTDOWN"

// BuiltinBlocked reports why the built-in OS shutdown may not run, or "".
// Test binaries are always blocked.
func BuiltinBlocked() string {
	if testing.Testing() {
		return "running inside a test"
	}
	if os.Getenv(NeverShutdownEnv) != "" {
		return NeverShutdownEnv + " is set"
	}
	return ""
}

// DefaultCommand returns the operating system's shutdown command.
func DefaultCommand() []string {
	switch runtime.GOOS {
	case "windows":
		// /d u:6:12 records "power failure" as the reason.
		return []string{"shutdown.exe", "/s", "/f", "/t", "0", "/d", "u:6:12"}
	case "freebsd":
		return []string{"/sbin/shutdown", "-p", "now"}
	case "darwin":
		return []string{"/sbin/shutdown", "-h", "now"}
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return []string{"systemctl", "poweroff"}
	}
	return []string{"/sbin/shutdown", "-h", "now"}
}

// NewExecutor returns the executor that really runs commands.
func NewExecutor(log *slog.Logger) Executor { return &osExecutor{log: log} }

type osExecutor struct{ log *slog.Logger }

func shell(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd.exe", "/C", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func (x *osExecutor) run(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if s := strings.TrimSpace(out.String()); s != "" {
		x.log.Info("command output", "command", strings.Join(cmd.Args, " "), "output", s)
	}
	return err
}

func (x *osExecutor) RunHook(ctx context.Context, command string, env []string) error {
	cmd := shell(ctx, command)
	cmd.Env = append(os.Environ(), env...)
	return x.run(cmd)
}

func (x *osExecutor) Shutdown(ctx context.Context, command string) error {
	if command != "" {
		return x.run(shell(ctx, command))
	}
	if why := BuiltinBlocked(); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	args := DefaultCommand()
	return x.run(exec.CommandContext(ctx, args[0], args[1:]...))
}
