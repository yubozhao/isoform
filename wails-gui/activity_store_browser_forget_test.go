package main

import "testing"

func TestDeleteAllBrowserActivityRemovesEveryBrowser(t *testing.T) {
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.InsertBrowserVisits([]BrowserVisitRow{
		{URL: "https://a.example", Title: "A", VisitedAt: 1000, Browser: "chrome", SourceVisitID: 1},
		{URL: "https://b.example", Title: "B", VisitedAt: 2000, Browser: "safari", SourceVisitID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAllBrowserActivity(); err != nil {
		t.Fatal(err)
	}
	visits, err := store.ListBrowserVisits(0, 10_000)
	if err != nil || len(visits) != 0 {
		t.Fatalf("visits after delete = %d %v", len(visits), err)
	}
	var days int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM browser_site_days`).Scan(&days)
	if days != 0 {
		t.Fatalf("site days after delete = %d", days)
	}
}
