// Package models owns the shipped catalog and confirmed model downloads.
package models

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed manifest.json evaluation.md
var assets embed.FS

type Licence struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	NonCommercial bool   `json:"nonCommercial"`
}
type Source struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}
type InstalledFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type LegacyInstall struct {
	Marker   string   `json:"marker"`
	Required []string `json:"required"`
}
type Archive struct {
	Legacy      *LegacyInstall  `json:"legacy,omitempty"`
	Format      string          `json:"format"`
	Destination string          `json:"destination"`
	Root        string          `json:"root"`
	Files       []InstalledFile `json:"files"`
}
type File struct {
	Path          string   `json:"path"`
	SHA256        *string  `json:"sha256"`
	Bytes         *int64   `json:"bytes"`
	URLs          []string `json:"urls"`
	Archive       *Archive `json:"archive,omitempty"`
	UnknownReason string   `json:"unknownReason,omitempty"`
	VerifiedFrom  string   `json:"verifiedFrom,omitempty"`
}
type Entry struct {
	ID            string   `json:"id"`
	Job           string   `json:"job"`
	DisplayName   string   `json:"displayName"`
	Line          string   `json:"line"`
	Evidence      string   `json:"evidence"`
	SizeBytes     *int64   `json:"sizeBytes"`
	Files         []File   `json:"files"`
	Sources       []Source `json:"sources"`
	Licence       Licence  `json:"licence"`
	Recommended   bool     `json:"recommended"`
	Languages     []string `json:"languages"`
	UnknownReason string   `json:"unknownReason,omitempty"`
}

func Catalog() []Entry {
	raw, _ := assets.ReadFile("manifest.json")
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		panic(err)
	}
	if err := Validate(entries); err != nil {
		panic(err)
	}
	return entries
}
func Evaluation() string { raw, _ := assets.ReadFile("evaluation.md"); return string(raw) }
func safePath(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && !strings.Contains(p, "\\") && filepath.ToSlash(filepath.Clean(p)) == p && p != ".." && !strings.HasPrefix(p, "../")
}
func Validate(entries []Entry) error {
	seen := map[string]bool{}
	jobs := map[string]bool{"live": true, "afterCall": true, "voice": true, "privacy": true, "memory": true, "speakerPack": true}
	paths := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" || seen[e.ID] || !jobs[e.Job] || e.DisplayName == "" || e.Licence.Name == "" {
			return fmt.Errorf("invalid model %q", e.ID)
		}
		seen[e.ID] = true
		var size int64
		known := true
		if len(e.Files) == 0 && e.ID != "none" {
			known = false
			if e.UnknownReason == "" {
				return fmt.Errorf("missing file metadata: %s", e.ID)
			}
		}
		for _, f := range e.Files {
			if !safePath(f.Path) || paths[f.Path] {
				return fmt.Errorf("invalid or shared model path %q", f.Path)
			}
			paths[f.Path] = true
			if f.SHA256 != nil {
				b, err := hex.DecodeString(*f.SHA256)
				if err != nil || len(b) != 32 {
					return fmt.Errorf("invalid checksum for %s", f.Path)
				}
			}
			if f.Bytes == nil || f.SHA256 == nil {
				known = false
				if f.UnknownReason == "" {
					return fmt.Errorf("unexplained unknown metadata: %s", f.Path)
				}
			} else {
				if *f.Bytes < 0 {
					return errors.New("negative file size")
				}
				size += *f.Bytes
			}
			if len(f.URLs) != len(e.Sources) {
				return fmt.Errorf("source mapping missing: %s", f.Path)
			}
			if f.Archive != nil {
				a := f.Archive
				if !safePath(a.Destination) || !safePath(a.Root) || a.Format != "tar.bz2" {
					return fmt.Errorf("invalid archive: %s", f.Path)
				}
				if a.Legacy != nil {
					if !safePath(a.Legacy.Marker) || len(a.Legacy.Required) == 0 {
						return fmt.Errorf("invalid legacy marker: %s", f.Path)
					}
					for _, required := range a.Legacy.Required {
						found := false
						for _, inside := range a.Files {
							if inside.Path == required {
								found = true
							}
						}
						if !found {
							return fmt.Errorf("unknown legacy member: %s", required)
						}
					}
				}
				for _, inside := range a.Files {
					if !safePath(inside.Path) || inside.Bytes < 0 || len(inside.SHA256) != 64 {
						return fmt.Errorf("invalid archive member: %s", inside.Path)
					}
				}
			}
		}
		if known && (e.SizeBytes == nil || *e.SizeBytes != size) {
			return fmt.Errorf("size total mismatch: %s", e.ID)
		}
		if len(e.Files) > 0 && (len(e.Sources) == 0 || e.Sources[0].Kind != "mirror" || !strings.HasPrefix(e.Sources[0].URL, "https://model-assets.yansu.app/")) {
			return fmt.Errorf("mirror must be first: %s", e.ID)
		}
	}
	return nil
}

var ErrNotReady = errors.New("model not downloaded; confirm its download in setup or Settings")

// Path recognizes legacy installations without moving or fetching their files.
func Path(configDir, relative string) string { return pathForEntries(configDir, relative, Catalog()) }
func pathForEntries(configDir, relative string, entries []Entry) string {
	if strings.HasPrefix(relative, "sherpa/") {
		legacy := filepath.Join(configDir, filepath.FromSlash(relative))
		for _, e := range entries {
			for _, f := range e.Files {
				if f.Path == relative && recognized(legacy, f.Bytes, f.SHA256) {
					return legacy
				}
				if a := f.Archive; a != nil {
					for _, inside := range a.Files {
						if filepath.ToSlash(filepath.Join(a.Destination, inside.Path)) == relative && presentAt(configDir, configDir, e) {
							return legacy
						}
					}
				}
			}
		}
	}
	return filepath.Join(configDir, "models", filepath.FromSlash(relative))
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func recognized(path string, bytes *int64, sha *string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return false
	}
	if bytes != nil && st.Size() != *bytes {
		return false
	}
	if sha != nil {
		return verifiedFile(path, st, *sha)
	}
	return false
}
func Present(configDir, id string) bool {
	for _, e := range Catalog() {
		if e.ID == id {
			return presentAt(filepath.Join(configDir, "models"), configDir, e)
		}
	}
	return false
}
func Require(configDir string, ids ...string) error {
	for _, id := range ids {
		if !Present(configDir, id) {
			return fmt.Errorf("%s: %w", id, ErrNotReady)
		}
	}
	return nil
}
func presentAt(folder, legacy string, e Entry) bool {
	if len(e.Files) == 0 && e.ID != "none" {
		return false
	}
	for _, f := range e.Files {
		if a := f.Archive; a != nil {
			if len(a.Files) == 0 {
				return false
			}
			if !archivePresent(folder, f) && !archivePresent(legacy, f) {
				return false
			}
		} else if !recognized(filepath.Join(folder, f.Path), f.Bytes, f.SHA256) && !(strings.HasPrefix(f.Path, "sherpa/") && recognized(filepath.Join(legacy, f.Path), f.Bytes, f.SHA256)) {
			return false
		}
	}
	return true
}

func archivePresent(folder string, f File) bool {
	a := f.Archive
	root := filepath.Join(folder, a.Destination)
	if legacy := a.Legacy; legacy != nil && f.SHA256 != nil {
		marker, err := os.ReadFile(filepath.Join(root, legacy.Marker))
		if err == nil && strings.TrimSpace(string(marker)) == *f.SHA256 {
			valid := len(legacy.Required) > 0
			for _, required := range legacy.Required {
				found := false
				for _, file := range a.Files {
					if file.Path == required {
						found = recognized(filepath.Join(root, file.Path), &file.Bytes, &file.SHA256)
						break
					}
				}
				valid = valid && found
			}
			if valid {
				return true
			}
		}
	}
	for _, file := range a.Files {
		if !recognized(filepath.Join(root, file.Path), &file.Bytes, &file.SHA256) {
			return false
		}
	}
	return len(a.Files) > 0
}

var verifiedFiles = struct {
	sync.Mutex
	files map[string]verifiedFileState
}{files: map[string]verifiedFileState{}}

type verifiedFileState struct {
	info   os.FileInfo
	change string
	sha    string
}

// A cached verification is tied to the file identity and kernel change time,
// as well as size and modification time. Size alone never establishes trust.
func verifiedFile(path string, before os.FileInfo, want string) bool {
	ok, _ := verifiedFileChecked(path, before, want)
	return ok
}

func verifiedFileChecked(path string, before os.FileInfo, want string) (bool, error) {
	change := fileChangeStamp(before)
	verifiedFiles.Lock()
	cached, ok := verifiedFiles.files[path]
	verifiedFiles.Unlock()
	if change != "" && ok && cached.sha == want && cached.change == change && os.SameFile(cached.info, before) && cached.info.Size() == before.Size() && cached.info.ModTime() == before.ModTime() {
		return true, nil
	}
	got, err := hashFile(path)
	after, statErr := os.Stat(path)
	if err != nil {
		return false, err
	}
	if statErr != nil {
		return false, statErr
	}
	if got != want || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || change != fileChangeStamp(after) {
		return false, nil
	}
	if change != "" {
		verifiedFiles.Lock()
		if len(verifiedFiles.files) >= 2048 {
			clear(verifiedFiles.files)
		}
		verifiedFiles.files[path] = verifiedFileState{after, change, want}
		verifiedFiles.Unlock()
	}
	return true, nil
}

// MemoryModelStatus reports the catalog download size and checks both pinned
// MiniLM files using the same verification cache as the inference runtime.
func MemoryModelStatus(configDir string) (bool, int64, error) {
	for _, e := range Catalog() {
		if e.ID != "minilm" {
			continue
		}
		ready := true
		for _, f := range e.Files {
			path := filepath.Join(configDir, "models", f.Path)
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				ready = false
				continue
			}
			if err != nil {
				return false, 0, err
			}
			if !info.Mode().IsRegular() {
				return false, 0, fmt.Errorf("expected model file at %s", path)
			}
			if info.Size() != *f.Bytes {
				ready = false
				continue
			}
			ok, err := verifiedFileChecked(path, info, *f.SHA256)
			if err != nil {
				return false, 0, err
			}
			ready = ready && ok
		}
		return ready, *e.SizeBytes, nil
	}
	return false, 0, fmt.Errorf("memory model is missing from catalog")
}
