package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// blockingMemoryQueryEmbedder stands in for an embedder busy with other work:
// it answers only when the caller's context ends.
type blockingMemoryQueryEmbedder struct {
	activityEmbeddingTestProvider
}

func (*blockingMemoryQueryEmbedder) EmbedQuery(ctx context.Context, _ string) ([]float64, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A bounded best-effort search (the triage dedup recall gives it 5 s) must
// still return keyword hits when the query embedding takes the whole budget.
// The app log showed the embedding phase spending it all, then every keyword
// search failing at once with "context deadline exceeded".
func TestMemorySearchKeepsKeywordBudgetWhenEmbeddingStalls(t *testing.T) {
	newPrivacyReadTestApp(t)
	store, err := NewMemoryStore(filepath.Join(os.Getenv("HOME"), "search"), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedMemoryReadBudgetCorpus(t, store, 20, 0, 1)
	store.embedder = &blockingMemoryQueryEmbedder{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results, err := store.Search(ctx, "needlework detail", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("a stalled embedder left the keyword search no budget")
	}
}

func TestMemoryVectorContextKeepsStrictAndShortBudgets(t *testing.T) {
	strictCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline, _ := strictCtx.Deadline()
	for name, tc := range map[string]struct {
		ctx    context.Context
		strict bool
		want   time.Time
	}{
		"strict keeps the full budget": {strictCtx, true, deadline},
		"best effort reserves time":    {strictCtx, false, deadline.Add(-memorySearchKeywordReserve)},
	} {
		got, done := memoryVectorContext(tc.ctx, tc.strict)
		d, _ := got.Deadline()
		done()
		if !d.Equal(tc.want) {
			t.Fatalf("%s: deadline %v, want %v", name, d, tc.want)
		}
	}
	short, cancelShort := context.WithTimeout(context.Background(), time.Second)
	defer cancelShort()
	shortDeadline, _ := short.Deadline()
	got, done := memoryVectorContext(short, false)
	defer done()
	if d, _ := got.Deadline(); !d.Equal(shortDeadline) {
		t.Fatalf("short budget deadline %v, want unchanged %v", d, shortDeadline)
	}
}
