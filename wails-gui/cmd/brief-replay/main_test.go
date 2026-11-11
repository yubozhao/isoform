package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// writeFixtureBrief stores a brief and its sealed copy under dir.
func writeFixtureBrief(t *testing.T, dir, id string, narrative int, print string, withheldReason string) {
	t.Helper()
	paras := ""
	for i := 0; i < narrative; i++ {
		if i > 0 {
			paras += ","
		}
		paras += `{"text":"private words"}`
	}
	brief := `{"briefId":"` + id + `","narrative":[` + paras + `],"cards":[{"title":"private card"}],"evidence":[{"ref":"r1"}]}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	copied := `{"schema":2,"print":"` + print + `","withheld_reason":"` + withheldReason + `","brief":` + brief + `}`
	if err := os.WriteFile(filepath.Join(dir, id+".read"), []byte(copied), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot hashes every file under dir (name, size and content), so a test can
// prove that a run changed nothing.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(name))
		h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		h.Write([]byte(info.ModTime().UTC().String()))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestReplayDecidesEachBriefWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	briefs := filepath.Join(dir, briefDirName)
	if err := os.MkdirAll(briefs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(briefs, privacyEpochFile), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Epoch 3 in base 36 is "3"; a print with epoch 2 needs amending.
	writeFixtureBrief(t, briefs, "2026-11-09-morning", 2, "3|false|false|abc", "")
	writeFixtureBrief(t, briefs, "2026-11-08-morning", 2, "2|false|false|abc", "")
	writeFixtureBrief(t, briefs, "2026-11-07-evening", 0, "3|false|false|abc", "source")
	writeFixtureBrief(t, briefs, "2026-11-06-morning", 1, "3|false|false|abc", "")
	if err := os.WriteFile(filepath.Join(briefs, "2026-11-05-morning.json"), []byte(`{"briefId":"2026-11-05-morning"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	before := snapshot(t, dir)
	results, err := replay(briefs)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, dir); after != before {
		t.Fatal("replay changed the data folder")
	}

	got := map[string]result{}
	for _, r := range results {
		got[r.ID] = r
	}
	cases := map[string]struct{ status, reason string }{
		"2026-11-09-morning": {"shown", "none"},
		"2026-11-08-morning": {"amend", "privacy_epoch_changed"},
		"2026-11-07-evening": {"withheld", "source"},
		"2026-11-06-morning": {"shown", "none"},
		"2026-11-05-morning": {"withheld", "no_copy"},
	}
	for id, want := range cases {
		r, ok := got[id]
		if !ok {
			t.Fatalf("brief %s was not replayed", id)
		}
		if r.Status != want.status || r.Reason != want.reason {
			t.Fatalf("brief %s: got %s/%s, want %s/%s", id, r.Status, r.Reason, want.status, want.reason)
		}
	}
	if len(results) != len(cases) {
		t.Fatalf("replayed %d briefs, want %d", len(results), len(cases))
	}
}

func TestReplayOfMissingFolderIsEmpty(t *testing.T) {
	results, err := replay(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(results) != 0 {
		t.Fatalf("missing folder: results=%d err=%v", len(results), err)
	}
}
