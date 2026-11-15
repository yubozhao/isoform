// Package onnxdl provides shared ONNX Runtime download and extraction helpers
// used by both the memory embedding system and the PII detection module.
package onnxdl

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	Version    = "1.24.4"
	VersionX86 = "1.23.0" // v1.24+ dropped macOS x86_64 builds
)

// RuntimePaths identifies the ONNX Runtime shared library to use.
type RuntimePaths struct {
	RuntimeDir string
	RuntimeLib string
	Bundled    bool
}

// LibName returns the platform-specific shared library filename.
func LibName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "libonnxruntime.so"
	}
}

// ResolveRuntimePaths returns the ONNX Runtime library path for this process.
// Packaged desktop builds already ship the runtime for sherpa-onnx; reusing
// that copy avoids loading a second native ONNX Runtime into the same process.
func ResolveRuntimePaths(configDir string) (*RuntimePaths, error) {
	if bundledLib, ok := BundledRuntimeLib(); ok {
		return &RuntimePaths{
			RuntimeDir: filepath.Dir(bundledLib),
			RuntimeLib: bundledLib,
			Bundled:    true,
		}, nil
	}

	runtimeDir := filepath.Join(configDir, "onnxruntime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}

	return &RuntimePaths{
		RuntimeDir: runtimeDir,
		RuntimeLib: filepath.Join(runtimeDir, LibName()),
	}, nil
}

// BundledRuntimeLib returns the ONNX Runtime library shipped with packaged
// Yansu builds, if one is available for the current executable.
func BundledRuntimeLib() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return bundledRuntimeLibForExecutable(runtime.GOOS, exe)
}

func bundledRuntimeLibForExecutable(goos, executablePath string) (string, bool) {
	switch goos {
	case "darwin":
		return bundledDarwinRuntimeLibForExecutable(executablePath)
	case "windows":
		lib := filepath.Join(filepath.Dir(executablePath), "onnxruntime.dll")
		if FileExists(lib) {
			return lib, true
		}
	}
	return "", false
}

func bundledDarwinRuntimeLibForExecutable(executablePath string) (string, bool) {
	contentsDir := darwinAppContentsDirForExecutable(executablePath)
	if contentsDir == "" {
		return "", false
	}

	frameworksDir := filepath.Join(contentsDir, "Frameworks")
	candidates := []string{
		filepath.Join(frameworksDir, fmt.Sprintf("libonnxruntime.%s.dylib", Version)),
		filepath.Join(frameworksDir, "libonnxruntime.dylib"),
	}
	for _, candidate := range candidates {
		if FileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func darwinAppContentsDirForExecutable(executablePath string) string {
	dir := filepath.Dir(filepath.Clean(executablePath))
	for {
		if filepath.Base(dir) == "Contents" {
			bundleDir := filepath.Dir(dir)
			if strings.HasSuffix(filepath.Base(bundleDir), ".app") {
				return dir
			}
			return ""
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// FileExists checks if a file exists at the given path.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}
