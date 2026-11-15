package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// replaceDirTmpSeq disambiguates the staging directory used by replaceDir
// so concurrent callers (e.g. two skill-hub installs racing on the same
// destination) can't clobber each other's in-flight copies.
var replaceDirTmpSeq atomic.Uint64

// sshShellInit is a shared prefix for SSH-executed shell commands. It
// ensures common tool directories are on PATH before running the
// user's command. Used by the remote-deploy path (not by the
// agent loop, which runs locally).
const sshShellInit = `export PATH="$HOME/.yansu/bin:$HOME/.bun/bin:$HOME/.local/bin:/usr/local/bin:$PATH"; `

func extractZipArchive(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	base := filepath.Clean(destDir) + string(os.PathSeparator)
	for _, f := range zr.File {
		target := filepath.Join(destDir, f.Name)
		cleanTarget := filepath.Clean(target)
		if !strings.HasPrefix(cleanTarget+string(os.PathSeparator), base) && cleanTarget != filepath.Clean(destDir) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func replaceDir(srcDir, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}

	// Unique staging path so two callers racing on the same destDir don't
	// clobber each other's tmp tree mid-copy. Sits next to destDir (same
	// parent) so the final rename remains a same-filesystem move.
	tmpDir := fmt.Sprintf("%s.tmp.%d.%d", destDir, time.Now().UnixNano(), replaceDirTmpSeq.Add(1))
	if err := copyDir(srcDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("copy directory: %w", err)
	}

	if err := os.RemoveAll(destDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("remove existing destination: %w", err)
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("promote temp directory: %w", err)
	}
	return nil
}

func copyDir(srcDir, destDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, relPath)

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.MkdirAll(target, 0o755)
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()

			dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			defer dst.Close()

			if _, err := io.Copy(dst, src); err != nil {
				return err
			}
			return nil
		}
	})
}
