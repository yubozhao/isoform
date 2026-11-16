package main

import "testing"

// A person's page is a snapshot (2026-11-15: 0.7 to 1.8 s per open as a live
// build). A write that does not go through the app's people paths does not
// rebuild it; the person's own edit does, on the next open.
func TestPersonDetailIsASnapshotThatPeopleEditsRefresh(t *testing.T) {
	app, _ := derivedTestApp(t)
	p := peopleTestContact(t, app, "Mei", "M Chen")
	first, err := app.GetPersonDetail(p.ID)
	if err != nil || first.Name != "Mei" {
		t.Fatalf("first open = %+v, %v", first, err)
	}
	row, err := app.store.GetPrincipal(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.DisplayName = "Mei (outside the app)"
	if _, err := app.store.UpdatePrincipal(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if again, _ := app.GetPersonDetail(p.ID); again.Name != "Mei" {
		t.Fatalf("second open rebuilt the page: %q", again.Name)
	}
	p.DisplayName = "Mei Chen"
	if _, err := app.UpdatePrincipal(p); err != nil {
		t.Fatal(err)
	}
	if edited, _ := app.GetPersonDetail(p.ID); edited.Name != "Mei Chen" {
		t.Fatalf("after the person's edit the page shows %q", edited.Name)
	}
}
