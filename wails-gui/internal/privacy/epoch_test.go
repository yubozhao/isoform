package privacy

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}

func TestEpochAdvanceSavesAndAdoptRestores(t *testing.T) {
	dir := t.TempDir()
	e := NewEpoch(writeTestFile)
	e.Adopt(dir)
	e.Advance(WithdrawnKey{Kind: "app", ID: "a"})
	e.Advance()
	if got := e.Load(); got != 2 {
		t.Fatalf("epoch = %d, want 2", got)
	}
	restarted := NewEpoch(writeTestFile)
	restarted.Adopt(dir)
	if got := restarted.Load(); got != 2 {
		t.Fatalf("epoch after restart = %d, want 2", got)
	}
	if keys := restarted.AllKeys(); len(keys) != 1 || keys[0].ID != "a" {
		t.Fatalf("keys after restart = %+v", keys)
	}
}

func TestEpochNeverGoesBack(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir, EpochFile, []byte("3")); err != nil {
		t.Fatal(err)
	}
	e := NewEpoch(writeTestFile)
	e.Store(7)
	e.Adopt(dir)
	if got := e.Load(); got != 7 {
		t.Fatalf("epoch = %d, want 7", got)
	}
}

func TestWithdrawnSinceAnswersOnlyFromKeyedEntries(t *testing.T) {
	e := NewEpoch(nil)
	e.Advance(WithdrawnKey{ID: "x"})
	e.Advance(WithdrawnKey{ID: "y"})
	if keys, ok := e.WithdrawnSince(1); !ok || len(keys) != 1 || keys[0].ID != "y" {
		t.Fatalf("since 1 = %+v %v", keys, ok)
	}
	e.Advance()
	if _, ok := e.WithdrawnSince(1); ok {
		t.Fatal("an unkeyed change must make the log unable to answer")
	}
	e.SwapLog(Log{Floor: 5})
	if _, ok := e.WithdrawnSince(4); ok {
		t.Fatal("a copy older than the floor must not be answered")
	}
}

func TestAdoptWithoutEntryForSavedEpochResetsLog(t *testing.T) {
	dir := t.TempDir()
	if err := writeTestFile(dir, EpochFile, []byte("9")); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(dir, LogFile, []byte(`{"floor":1,"entries":[{"epoch":2,"keys":[{"id":"z"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	e := NewEpoch(writeTestFile)
	e.Adopt(dir)
	if _, ok := e.WithdrawnSince(2); ok {
		t.Fatal("a saved epoch past the log's last entry must reset the log")
	}
	if loaded, epoch := e.Adopted(); !loaded || epoch != 9 {
		t.Fatalf("adopted = %v %d", loaded, epoch)
	}
}

func TestLogKeepsTheLastEntries(t *testing.T) {
	e := NewEpoch(nil)
	for i := 0; i < LogLimit+3; i++ {
		e.Advance(WithdrawnKey{ID: "k"})
	}
	if _, ok := e.WithdrawnSince(2); ok {
		t.Fatal("entries past the limit must move the floor")
	}
	if keys, ok := e.WithdrawnSince(3); !ok || len(keys) != LogLimit {
		t.Fatalf("since 3 = %d %v", len(keys), ok)
	}
}
