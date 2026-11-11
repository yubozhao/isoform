package main

import (
	"testing"

	"wails-gui/storage"
)

// Restored from feat (TestMobileBriefCitedPersonEditAndRemoval, removed on the seal
// v2 branch): deleting a person is a privacy action that names that person, so
// the briefs citing them are amended. An edit to a person is not a withdrawal.
func TestDeletedPersonIsRecordedAsWithdrawn(t *testing.T) {
	app, svc, _ := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	person, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "fictional-person", Kind: storage.PrincipalKindContact, DisplayName: "Original name"})
	if err != nil {
		t.Fatal(err)
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	if err := app.DeletePrincipal(person.ID); err != nil {
		t.Fatal(err)
	}
	keys, ok := withdrawnSince(epoch)
	if !ok {
		t.Fatal("the deletion of a person was not logged with its key")
	}
	found := false
	for _, key := range keys {
		if key.ID == person.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("deleting a person did not withdraw it: keys=%+v", keys)
	}
}
