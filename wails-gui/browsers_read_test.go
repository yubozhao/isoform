package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The browsers named on What Isoform sees › Screen are the ones the history
// reader reads (answers v1.5.0 T16): a profile with a History database, each
// browser once, in the reader's order, and never a protected one.
func TestBrowsersReadNames(t *testing.T) {
	home := t.TempDir()
	history := func(parts ...string) {
		dir := filepath.Join(append([]string{home, "Library", "Application Support"}, parts...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "History"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := browsersReadNames(home, nil); len(got) != 0 || got == nil {
		t.Fatalf("no browsers: got %#v, want an empty list", got)
	}
	history("Arc", "User Data", "Profile 1")
	history("Google", "Chrome", "Default")
	history("Google", "Chrome", "Profile 2")
	// A folder with no History database isn't read.
	if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := browsersReadNames(home, nil), []string{"Chrome", "Arc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	protectArc := func(bundleID, name string) bool { return name == "Arc" }
	if got, want := browsersReadNames(home, protectArc), []string{"Chrome"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with Arc protected: got %v, want %v", got, want)
	}
}
