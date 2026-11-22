package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"wails-gui/internal/sop"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// sopImportMaxZipBytes caps imported archives: an SOP bundle is a markdown
// document plus a handful of helper scripts, never megabytes of payload.
const sopImportMaxZipBytes = 20 << 20 // 20 MB

// ExportSOP zips an SOP bundle to a user-chosen location so it can be shared.
// Returns the written path, or "" when the user cancels the dialog. Wails
// binding.
func (a *App) ExportSOP(id string) (string, error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return "", errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return "", fmt.Errorf("sop store not ready")
	}
	e, err := a.sopStore.Get(id)
	if err != nil {
		return "", fmt.Errorf("load sop %s: %w", id, err)
	}

	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export SOP",
		DefaultFilename: e.ID + ".sop.zip",
		Filters: []runtime.FileFilter{
			{DisplayName: "SOP bundle (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if strings.TrimSpace(dest) == "" {
		return "", nil // user canceled
	}

	if err := zipDirectory(a.sopStore.PathFor(id), dest); err != nil {
		return "", fmt.Errorf("zip sop %s: %w", id, err)
	}
	if a.telemetry != nil {
		a.telemetry.Capture("sop.exported", map[string]any{
			"scripts_count": len(e.Scripts),
		})
	}
	log.Printf("[sop] exported %s to %s", id, dest)
	return dest, nil
}

// ImportSOP installs a shared SOP bundle from a user-chosen zip. Always
// creates a new bundle (unique slug); imported SOPs arrive as confirmed —
// they are curated artifacts someone chose to share, not machine drafts.
// Returns a zero Entry (ID=="") when the user cancels. Wails binding.
func (a *App) ImportSOP() (sop.Entry, error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return sop.Entry{}, errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return sop.Entry{}, fmt.Errorf("sop store not ready")
	}

	src, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import SOP",
		Filters: []runtime.FileFilter{
			{DisplayName: "SOP bundle (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return sop.Entry{}, fmt.Errorf("open dialog: %w", err)
	}
	if strings.TrimSpace(src) == "" {
		return sop.Entry{}, nil // user canceled
	}
	if info, serr := os.Stat(src); serr != nil {
		return sop.Entry{}, fmt.Errorf("stat archive: %w", serr)
	} else if info.Size() > sopImportMaxZipBytes {
		return sop.Entry{}, fmt.Errorf("archive too large (%d bytes, max %d)", info.Size(), sopImportMaxZipBytes)
	}

	tmp, err := os.MkdirTemp("", "yansu-sop-import-*")
	if err != nil {
		return sop.Entry{}, fmt.Errorf("create import dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := extractZipArchive(src, tmp); err != nil {
		return sop.Entry{}, fmt.Errorf("extract archive: %w", err)
	}
	draftDir, err := locateSOPBundleRoot(tmp)
	if err != nil {
		return sop.Entry{}, err
	}

	raw, err := os.ReadFile(filepath.Join(draftDir, "SOP.md"))
	if err != nil {
		return sop.Entry{}, fmt.Errorf("read SOP.md: %w", err)
	}
	drafted := sop.Parse("", string(raw))
	title := strings.TrimSpace(drafted.Title)
	if title == "" {
		title = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(src), ".zip"), ".sop")
	}

	slug := a.sopStore.UniqueSlug(title)
	saved, err := a.sopStore.InstallBundle(slug, draftDir, importedSOPMetadata())
	if err != nil {
		return sop.Entry{}, fmt.Errorf("install imported sop: %w", err)
	}

	a.emitBrowserEvent("sop:updated", map[string]any{"id": saved.ID, "status": string(saved.Status)})
	if a.telemetry != nil {
		a.telemetry.Capture("sop.imported", map[string]any{
			"scripts_count": len(saved.Scripts),
		})
	}
	log.Printf("[sop] imported %s from %s", saved.ID, src)
	return saved, nil
}

// Imported metadata is not locally attested source proof. The artifact remains
// available for local inspection, without laundering its source restrictions.
func importedSOPMetadata() sop.Entry {
	return sop.Entry{Status: sop.StatusConfirmed, Source: sop.SourceImported,
		ReadProvenance: sopReadProvenanceJSON(sopReadProvenance{Version: 1, ReadBlocked: true})}
}

// locateSOPBundleRoot finds the directory containing SOP.md inside an
// extracted archive: either the extraction root itself, or a single
// top-level folder (people naturally zip the bundle directory).
func locateSOPBundleRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "SOP.md")); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, de := range entries {
		if de.IsDir() && !strings.HasPrefix(de.Name(), ".") && de.Name() != "__MACOSX" {
			dirs = append(dirs, de.Name())
		}
	}
	if len(dirs) == 1 {
		nested := filepath.Join(dir, dirs[0])
		if _, err := os.Stat(filepath.Join(nested, "SOP.md")); err == nil {
			return nested, nil
		}
	}
	return "", fmt.Errorf("archive is not an SOP bundle (no SOP.md found)")
}

// zipDirectory writes srcDir's regular files into a zip at destPath, with
// paths relative to srcDir. Symlinks and special files are skipped, matching
// the bundle installer's copy rules.
func zipDirectory(srcDir, destPath string) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}
