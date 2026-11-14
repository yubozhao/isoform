package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wails-gui/storage"
)

func derivedDiskFileViews(t *testing.T) map[string]bool {
	t.Helper()
	views := map[string]bool{}
	for _, path := range derivedDiskFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var file derivedDiskFile
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		views[file.View] = true
	}
	return views
}

// The person's own change (a touch, such as removing imported contacts)
// deletes that view's saved copy in the same step: a quit before the rebuild
// saves must not paint the old page on the next launch.
func TestDerivedSnapshotTouchDeletesTheSavedCopy(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	saved := func() (derivedTestValue, error) { return derivedTestValue{Items: []string{"saved"}}, nil }
	readDerivedTestValue(t, app, derivedPeopleOverview, saved)
	readDerivedTestValue(t, app, derivedAboutYou, saved)
	if views := derivedDiskFileViews(t); !views[string(derivedPeopleOverview)] || !views[string(derivedAboutYou)] {
		t.Fatalf("saved views = %v, want people-overview and about-you", views)
	}
	app.touchDerivedViews(derivedPeopleOverview)
	views := derivedDiskFileViews(t)
	if views[string(derivedPeopleOverview)] {
		t.Fatal("a touched view kept its saved copy")
	}
	if !views[string(derivedAboutYou)] {
		t.Fatal("an untouched view lost its saved copy")
	}
	restarted := restartedDerivedApp(t, app)
	got := readDerivedTestValue(t, restarted, derivedPeopleOverview, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"live"}}, nil
	})
	if len(got.Items) != 1 || got.Items[0] != "live" {
		t.Fatalf("after a touch and a restart got %v, want a live build", got.Items)
	}
}

// A memory, know-how file or person deleted outside the app's privacy actions
// (Finder, a sync tool, while the app was closed) makes every saved copy
// built while it existed unservable.
func TestDerivedSnapshotNotServedAfterASourceIsDeleted(t *testing.T) {
	for _, kind := range []string{"memory", "knowledge", "person"} {
		t.Run(kind, func(t *testing.T) {
			app, _ := derivedDiskTestApp(t)
			memDir, err := memoryDir()
			if err != nil {
				t.Fatal(err)
			}
			knowDir, err := knowledgeDir()
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{memDir, knowDir} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			memoryFile := filepath.Join(memDir, "deleted-belief.md")
			knowFile := filepath.Join(knowDir, "deleted-howto.md")
			for _, path := range []string{memoryFile, knowFile, filepath.Join(memDir, "kept-belief.md")} {
				if err := os.WriteFile(path, []byte("# Item\n\n- A point.\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			person, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Alex"})
			if err != nil {
				t.Fatal(err)
			}
			readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
				return derivedTestValue{Items: []string{"saved"}}, nil
			})
			live := func() (derivedTestValue, error) { return derivedTestValue{Items: []string{"live"}}, nil }

			// Adding a source keeps the copy servable.
			if err := os.WriteFile(filepath.Join(memDir, "new-belief.md"), []byte("# New\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := readDerivedTestValue(t, restartedDerivedApp(t, app), derivedAboutYou, live); got.Items[0] != "saved" {
				t.Fatalf("an added source dropped the saved copy: %v", got.Items)
			}

			switch kind {
			case "memory":
				err = os.Remove(memoryFile)
			case "knowledge":
				err = os.Remove(knowFile)
			case "person":
				// Straight in the store, as a sync would, with no withdrawal.
				err = app.store.DeletePrincipal(t.Context(), person.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := readDerivedTestValue(t, restartedDerivedApp(t, app), derivedAboutYou, live)
			if len(got.Items) != 1 || got.Items[0] != "live" {
				t.Fatalf("a saved copy was served after its %s source was deleted: %v", kind, got.Items)
			}
		})
	}
}
