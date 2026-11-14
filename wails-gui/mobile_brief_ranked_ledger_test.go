package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBriefMissingLedgerProofDistinguishesUnavailableStore(t *testing.T) {
	dir := t.TempDir()
	store := newMobileBriefStore(dir)
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	proofs := []morningBriefLedgerProof{{BriefID: "2026-10-29-morning"}}
	if !morningBriefMissingLedgerProof(store, proofs) {
		t.Fatal("missing historical brief was not recognized")
	}
	path := filepath.Join(store.dir, proofs[0].BriefID+".json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if morningBriefMissingLedgerProof(store, proofs) {
		t.Fatal("existing historical brief was reported missing")
	}
	if morningBriefMissingLedgerProof(newMobileBriefStore(filepath.Join(dir, "unavailable")), proofs) {
		t.Fatal("unavailable store was reported as a missing proof")
	}
}
