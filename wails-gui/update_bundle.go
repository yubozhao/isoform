package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Each system tool has a bounded run time; no PATH lookup can substitute it.
func updateTool(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// verifyUpdateBundleRequirement runs every check and returns the installed
// app's designated requirement the candidate satisfied.
func verifyUpdateBundleRequirement(current, candidate, version string) (string, error) {
	return verifyUpdateBundleWithToolsRequirement(current, candidate, version, updateTool, stripQuarantineRecursive)
}

func verifyUpdateBundleWithTools(current, candidate, version string, run func(string, ...string) ([]byte, error), strip func(string) (int, error)) error {
	_, err := verifyUpdateBundleWithToolsRequirement(current, candidate, version, run, strip)
	return err
}

func verifyUpdateBundleWithToolsRequirement(current, candidate, version string, run func(string, ...string) ([]byte, error), strip func(string) (int, error)) (string, error) {
	requirement, err := installedUpdateRequirement(current, run)
	if err != nil {
		return "", err
	}
	if _, err := run("/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+requirement, candidate); err != nil {
		return "", err
	}
	// Signature identity is established before clearing inherited quarantine.
	// Gatekeeper then checks notarization without requiring Xcode/stapler on
	// the user's computer. Mere acceptance of an old Developer ID is insufficient.
	if _, err := strip(candidate); err != nil {
		return "", err
	}
	out, err := run("/usr/sbin/spctl", "--assess", "--type", "execute", "--verbose=2", candidate)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(out), "source=Notarized Developer ID") {
		return "", fmt.Errorf("update app has no notarized Developer ID assessment")
	}
	if err := checkUpdateBundleVersion(candidate, version, run); err != nil {
		return "", err
	}
	return requirement, nil
}

// installedUpdateRequirement is the trust anchor: the installed app's
// designated requirement (identifier + team), read only from a Developer ID
// signed app. Ad-hoc or unsigned source apps are refused.
func installedUpdateRequirement(current string, run func(string, ...string) ([]byte, error)) (string, error) {
	developerID := "anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"
	if _, err := run("/usr/bin/codesign", "--verify", "--strict", "-R="+developerID, current); err != nil {
		return "", err
	}
	out, err := run("/usr/bin/codesign", "-d", "-r-", current)
	if err != nil {
		return "", err
	}
	requirement := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "designated => ") {
			requirement = strings.TrimPrefix(line, "designated => ")
		}
	}
	if requirement == "" {
		return "", fmt.Errorf("installed app has no designated signing requirement")
	}
	return requirement, nil
}

func checkUpdateBundleVersion(candidate, version string, run func(string, ...string) ([]byte, error)) error {
	out, err := run("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", filepath.Join(candidate, "Contents", "Info.plist"))
	if err != nil {
		return err
	}
	fullVersion := strings.TrimPrefix(version, "v")
	if _, ok := parseUpdateVersion(fullVersion); !ok || strings.TrimSpace(string(out)) != strings.SplitN(fullVersion, "-", 2)[0] {
		return fmt.Errorf("signed app version does not match update feed %q", version)
	}
	if strings.Contains(fullVersion, "-") {
		out, err = run("/usr/libexec/PlistBuddy", "-c", "Print :IsoformReleaseVersion", filepath.Join(candidate, "Contents", "Info.plist"))
		if err != nil || strings.TrimSpace(string(out)) != fullVersion {
			return fmt.Errorf("signed app prerelease version does not match update feed %q", version)
		}
	}
	return nil
}

// quickVerifyPreparedBundle runs at the click. The deep signature check,
// quarantine strip and Gatekeeper notarization check ran when the update was
// prepared; this confirms the prepared app is still the app that was
// verified (its sealed executable and resources), that it still satisfies the
// installed app's identity, and that it is still the feed's version.
func quickVerifyPreparedBundle(current, candidate, version, requirement string) error {
	return quickVerifyPreparedBundleWithTools(current, candidate, version, requirement, updateTool)
}

func quickVerifyPreparedBundleWithTools(current, candidate, version, requirement string, run func(string, ...string) ([]byte, error)) error {
	if requirement == "" {
		return fmt.Errorf("prepared update has no recorded signing requirement")
	}
	// The installed app and the candidate are checked at the same time: each
	// codesign run hashes a whole sealed bundle, about 0.35 s apiece.
	var installed string
	var installedErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		installed, installedErr = installedUpdateRequirement(current, run)
	}()
	_, candidateErr := run("/usr/bin/codesign", "--verify", "--strict", "-R="+requirement, candidate)
	<-done
	if installedErr != nil {
		return installedErr
	}
	if installed != requirement {
		return fmt.Errorf("installed app's signing requirement changed since the update was prepared")
	}
	if candidateErr != nil {
		return candidateErr
	}
	return checkUpdateBundleVersion(candidate, version, run)
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// The release pipeline packages one full .app.tar.gz. Reject other layouts and
// unsafe entries rather than following links while writing archive contents.
func extractAppFromTarGz(r io.Reader, destDir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	app := ""
	var links []*tar.Header
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(h.Name)
		if name == "." && h.Typeflag == tar.TypeDir {
			continue
		}
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("unsafe archive path %q", h.Name)
		}
		top := strings.Split(name, string(filepath.Separator))[0]
		// macOS tar stores extended attributes as AppleDouble metadata.
		// Native extraction below restores it; do not materialize ._ files
		// in the signed app during the portable safety preflight.
		metadata := h.Typeflag == tar.TypeReg && strings.HasPrefix(filepath.Base(name), "._")
		if metadata && !strings.Contains(name, string(filepath.Separator)) {
			top = strings.TrimPrefix(top, "._")
		}
		if !strings.HasSuffix(top, ".app") {
			return "", fmt.Errorf("archive entry outside app: %q", h.Name)
		}
		root := filepath.Join(destDir, top)
		if app != "" && app != root {
			return "", fmt.Errorf("archive contains multiple app bundles")
		}
		app = root
		target := filepath.Join(destDir, name)
		total += h.Size
		if h.Size < 0 || total > 8<<30 {
			return "", fmt.Errorf("update archive exceeds extraction limit")
		}
		if metadata {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, os.FileMode(h.Mode).Perm()|0700)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", err
			}
			var f *os.File
			// Duplicate files (including file/dir collisions) are refused.
			f, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode).Perm())
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return "", copyErr
			}
			err = closeErr
		case tar.TypeSymlink:
			copy := *h
			links = append(links, &copy)
		default:
			return "", fmt.Errorf("unsupported update archive entry %q", h.Name)
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return "", err
	}
	if app == "" {
		return "", fmt.Errorf("no .app bundle found in archive")
	}
	for _, h := range links {
		target := filepath.Join(destDir, filepath.Clean(h.Name))
		if filepath.IsAbs(h.Linkname) || !pathInside(app, filepath.Join(filepath.Dir(target), h.Linkname)) {
			return "", fmt.Errorf("unsafe app symlink %q", h.Name)
		}
		// No symlink is allowed in a parent of another archive entry.
		for parent := filepath.Dir(target); pathInside(app, parent); parent = filepath.Dir(parent) {
			if st, err := os.Lstat(parent); err == nil && st.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("symlink parent %q", parent)
			}
			if parent == app {
				break
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}
		if err := os.Symlink(h.Linkname, target); err != nil {
			return "", err
		}
	}
	for _, h := range links {
		resolved, err := filepath.EvalSymlinks(filepath.Join(destDir, filepath.Clean(h.Name)))
		if err != nil || !pathInside(app, resolved) {
			return "", fmt.Errorf("app symlink escapes bundle or is broken: %q", h.Name)
		}
	}
	return app, nil
}

// Preflight all paths with the portable extractor, then use the system tar on
// macOS so stapled tickets, resource forks and other extended attributes survive.
func extractAppArchiveFile(archive, root string) (string, error) {
	validation := filepath.Join(root, "validated")
	if err := os.Mkdir(validation, 0700); err != nil {
		return "", err
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	app, extractErr := extractAppFromTarGz(f, validation)
	closeErr := f.Close()
	if extractErr != nil {
		return "", extractErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if runtime.GOOS != "darwin" {
		return app, nil
	}
	native := filepath.Join(root, "unpacked")
	if err := os.Mkdir(native, 0700); err != nil {
		return "", err
	}
	if _, err := updateTool("/usr/bin/tar", "-xzf", archive, "-C", native); err != nil {
		return "", err
	}
	_ = os.RemoveAll(validation)
	return filepath.Join(native, filepath.Base(app)), nil
}
