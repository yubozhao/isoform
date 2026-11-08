//go:build !windows

package main

import (
	"context"
	"os/exec"

	"wails-gui/internal/platform/cmdutil"
)

func cliProbeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return cmdutil.CommandContext(ctx, name, args...)
}
