//go:build windows

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"wails-gui/internal/platform/cmdutil"
)

// npm installs .cmd shims on Windows. Probe arguments are code-owned version
// and sign-in status commands; cmd.exe needs its own quoting for these shims.
func cliProbeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	path, err := exec.LookPath(name)
	if err != nil || (strings.ToLower(filepath.Ext(path)) != ".cmd" && strings.ToLower(filepath.Ext(path)) != ".bat") {
		return cmdutil.CommandContext(ctx, name, args...)
	}
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	parts := []string{quote(path)}
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	cmd := cmdutil.CommandContext(ctx, "cmd.exe")
	cmd.SysProcAttr.CmdLine = quote(cmd.Path) + ` /D /S /C "` + strings.Join(parts, " ") + `"`
	return cmd
}
