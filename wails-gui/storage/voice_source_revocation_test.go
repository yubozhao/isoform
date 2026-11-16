package storage

import (
	"context"
	"testing"
)

// The saved first chat page follows the revocation counter: a sync adding or
// updating source rows keeps it, while a removal, a person edit or a
// permission switch moves it. The full source counter still moves on every
// write.
func TestVoiceSourceRevocationIgnoresSyncWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	read := func() (int64, int64) {
		t.Helper()
		all, err := s.VoiceSourceVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		revoked, err := s.VoiceSourceRevocationVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return all, revoked
	}
	all0, revoked0 := read()
	if err := s.UpsertReminders(ctx, []ReminderRow{{UID: "r1", Title: "call"}}, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertReminders(ctx, []ReminderRow{{UID: "r1", Title: "call back"}}, 20); err != nil {
		t.Fatal(err)
	}
	all1, revoked1 := read()
	if all1 == all0 || revoked1 != revoked0 {
		t.Fatalf("sync writes: all %d->%d, revocation %d->%d; want all to move and revocation to hold", all0, all1, revoked0, revoked1)
	}
	if _, err := s.DeleteAllReminders(ctx); err != nil {
		t.Fatal(err)
	}
	_, revoked2 := read()
	if revoked2 == revoked1 {
		t.Fatal("a removed source row kept the revocation counter")
	}
	if err := s.SetUIState(ctx, "permission_enabled:reminders", "false"); err != nil {
		t.Fatal(err)
	}
	_, revoked3 := read()
	if revoked3 == revoked2 {
		t.Fatal("a permission switch kept the revocation counter")
	}
	p, err := s.CreatePrincipal(ctx, PrincipalRow{DisplayName: "Sam", Kind: PrincipalKindContact})
	if err != nil {
		t.Fatal(err)
	}
	_, revoked4 := read()
	p.DisplayName = "Sam Lee"
	if _, err := s.UpdatePrincipal(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, revoked5 := read(); revoked5 == revoked4 {
		t.Fatal("a person edit kept the revocation counter")
	}
}
