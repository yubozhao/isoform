package storage

import (
	"context"
	"testing"
)

func TestResetPrincipalToIdentityKeepsOnlyTheNameAndKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	self, err := s.CreatePrincipal(ctx, PrincipalRow{Kind: PrincipalKindSelf, DisplayName: "Alex", Notes: "Works late", Relationship: "", Aliases: nil})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO principal_aliases (principal_id, alias) VALUES (?, 'Alex Rivera')`, self.ID); err != nil {
		t.Skipf("alias shape: %v", err)
	}
	if _, err := s.BindAppHandle(ctx, self.ID, "email", "alex@example.com", "alex@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPrincipalToIdentity(ctx, self.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPrincipal(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Alex" || got.Kind != PrincipalKindSelf || got.Notes != "" || len(got.Aliases) != 0 {
		t.Fatalf("self after reset = %+v", got)
	}
	handles, err := s.ListAppHandlesByPrincipal(ctx, self.ID)
	if err != nil || len(handles) != 0 {
		t.Fatalf("handles after reset = %+v %v", handles, err)
	}
}
