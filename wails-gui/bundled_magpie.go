package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// magpie (https://usemagpie.ai) is a local model gateway that Yansu runs as
// a sidecar for managed model accounts. build-wails.sh copies the arch
// matching binary from wails-gui/magpie-bundle/ into the app as
// Contents/Resources/magpie-bundle/magpie-cli, where the generic embedded
// code walk signs it with the other nested binaries. Dev runs read
// wails-gui/magpie-bundle/ directly; scripts/download-magpie.sh fills it.
//
// The binary runs in place (like cua-driver): code signing and TCC identity
// are bound to the bundle location, so it is never copied out.

const (
	magpieBundleDirName = "magpie-bundle"
	// magpieOverrideEnv forces a specific binary (tests and debugging). An
	// override that does not point at an executable is an error, not a
	// silent fallback to the other candidates.
	magpieOverrideEnv = "YANSU_MAGPIE_BIN"
)

// MagpieNotFoundError is returned by LocateBundledMagpie when no usable
// binary exists. Searched lists the candidate paths that were checked.
type MagpieNotFoundError struct {
	Searched []string
}

func (e *MagpieNotFoundError) Error() string {
	return fmt.Sprintf("magpie binary not found (searched %s and PATH)", strings.Join(e.Searched, ", "))
}

// magpieAppBinaryName is the name build-wails.sh gives the embedded copy.
func magpieAppBinaryName() string {
	if runtime.GOOS == "windows" {
		return "magpie-cli.exe"
	}
	return "magpie-cli"
}

// magpieDevBinaryName is the arch-suffixed name download-magpie.sh writes
// into wails-gui/magpie-bundle/.
func magpieDevBinaryName() string {
	name := fmt.Sprintf("magpie-cli-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// LocateBundledMagpie returns the path of a runnable magpie binary. Order:
//  1. YANSU_MAGPIE_BIN (must be executable, otherwise an error)
//  2. Yansu.app/Contents/Resources/magpie-bundle/magpie-cli, and the copy
//     next to the executable
//  3. wails-gui/magpie-bundle/magpie-cli-<goos>-<goarch> relative to the
//     working directory (repo root or wails-gui/), for dev runs
//  4. magpie on PATH
func LocateBundledMagpie() (string, error) {
	if override := strings.TrimSpace(os.Getenv(magpieOverrideEnv)); override != "" {
		if isExecutableFile(override) {
			return override, nil
		}
		return "", &MagpieNotFoundError{Searched: []string{override}}
	}

	candidates := magpieBundleCandidates()
	for _, candidate := range candidates {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	if onPath, err := exec.LookPath("magpie"); err == nil {
		return onPath, nil
	}
	return "", &MagpieNotFoundError{Searched: candidates}
}

// magpieBundleCandidates lists the bundled locations in search order,
// excluding the PATH fallback.
func magpieBundleCandidates() []string {
	candidates := make([]string, 0, 4)
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, magpieBundleDirName, magpieAppBinaryName()),
			filepath.Join(exeDir, "..", "Resources", magpieBundleDirName, magpieAppBinaryName()),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, magpieBundleDirName, magpieDevBinaryName()),
			filepath.Join(wd, "wails-gui", magpieBundleDirName, magpieDevBinaryName()),
		)
	}
	return candidates
}
