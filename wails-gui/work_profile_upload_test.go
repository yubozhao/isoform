package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkProfilePrepareUploadUsesRenamedSpecialty(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	payload, err := workProfilePrepareUpload([]workProfileUploadGroup{{Name: "Client SOWs", Selected: true, Lines: []string{"Price and dates go first."}}}, []string{"You write clearly."}, "2026-11-08", 2, "Mina", []string{"owner@example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil || string(data) != `{"lines":["Client SOWs: Price and dates go first.","You write clearly."],"publicSpecialties":["Client SOWs"],"learnedThrough":"2026-11-08","expectedVersion":2}` {
		t.Fatalf("unexpected upload: %s, %v", data, err)
	}
	if _, err := workProfilePrepareUpload([]workProfileUploadGroup{{Name: "Client SOWs", Selected: true}}, []string{"You write clearly."}, "", 2, "", nil, now); err == nil {
		t.Fatal("selected specialty without a line was accepted")
	}
	if _, err := workProfilePrepareUpload([]workProfileUploadGroup{{Name: "Client SOWs", Selected: true, Lines: []string{" "}}}, nil, "", 2, "", nil, now); err == nil {
		t.Fatal("empty specialty line was accepted")
	}
}

func TestWorkProfileUploadValidation(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	valid := WorkProfileUpload{Lines: []string{"You write clearly."}, PublicSpecialties: []string{}, ExpectedVersion: 0}
	if err := workProfileValidateUpload(valid, "Mina", nil, now); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []WorkProfileUpload{
		{Lines: []string{"You write for Mina."}},
		{Lines: []string{"You write 12 updates."}},
		{Lines: []string{"You write\nupdates."}},
		{Lines: []string{strings.Repeat("a", 1001)}},
		{Lines: []string{"You write clearly."}, PublicSpecialties: []string{"updates"}},
		{Lines: []string{"You write clearly."}, LearnedThrough: "2026-11-10"},
	} {
		if err := workProfileValidateUpload(bad, "Mina", nil, now); err == nil {
			t.Fatalf("accepted invalid payload: %+v", bad)
		}
	}
}
