package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAnswersExportKeepsEveryAnswerOnItsOwnLine(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "answers.jsonl")
	entries := []AnswerRecord{
		{EntryID: "a1", ItemID: "belief:m1", Kind: answerKindBelief, Title: "Prefers mornings", Action: workActionYes},
		{EntryID: "a2", ItemID: "portrait", Kind: answerKindPortrait, Action: workActionNo, Note: "Not a runner"},
	}
	if err := writeAnswersExport(dest, entries); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("export file = %v %v, want private", info, err)
	}
	file, _ := os.Open(dest)
	defer file.Close()
	var got []AnswerRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record AnswerRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		got = append(got, record)
	}
	if len(got) != 2 || got[0].Title != "Prefers mornings" || got[1].Note != "Not a runner" {
		t.Fatalf("exported = %+v", got)
	}
}
