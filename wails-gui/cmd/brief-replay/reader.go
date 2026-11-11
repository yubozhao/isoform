package main

import (
	"os"
	"strings"
	"time"

	briefpkg "wails-gui/internal/brief"
)

// replayWithReader is replay with the decision and the counts read from
// internal/brief. It prints the same fields in the same format, so the old and
// the new output can be compared line by line on one folder. It reads only.
//
// The Reader has no privacy print here, so it compares the saved epoch only,
// as replay does. A copy with a different epoch is reported as amend.
func replayWithReader(dir string) ([]result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	reader := briefpkg.NewReader(briefpkg.ReaderOptions{Dir: dir})
	withdrawn := reader.Withdrawn()
	now := time.Now()
	var out []result
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !briefIDPattern.MatchString(id) {
			continue
		}
		out = append(out, decideWithReader(reader, id, withdrawn, now))
	}
	return out, nil
}

// decideWithReader maps one Inspection to the replay result. The mapping follows
// decide in main.go, including which fields a withheld or unreadable copy leaves empty.
func decideWithReader(reader *briefpkg.Reader, id string, withdrawn []briefpkg.Withdrawn, now time.Time) result {
	insp := reader.Inspect(id)
	r := result{ID: id, Migrate: "none", BuiltOK: insp.BuiltOK}
	if insp.BuiltOK {
		r.Built = [3]int{insp.Built.Narrative, insp.Built.Cards, insp.Built.Evidence}
	}
	switch insp.Decision {
	case briefpkg.DecisionNoCopy:
		r.Status, r.Reason = "withheld", "no_copy"
		return r
	case briefpkg.DecisionUnreadable, briefpkg.DecisionOlderSchema:
		r.Status, r.Reason = "withheld", "unreadable_copy"
		return r
	}
	r.Counts = [3]int{insp.Copy.Narrative, insp.Copy.Cards, insp.Copy.Evidence}
	r.Audio = insp.Copy.Audio
	r.Kind = insp.Withheld.SourceKind
	var built briefpkg.Document
	if insp.BuiltOK {
		built, _ = reader.Built(id)
	}
	r.Migrate = migrateWithReader(insp, built, insp.BuiltOK, withdrawn, now)
	switch insp.Decision {
	case briefpkg.DecisionAmendKeyed, briefpkg.DecisionAmendGates:
		r.Status, r.Reason = "amend", "privacy_epoch_changed"
		return r
	}
	if insp.Copy.Narrative == 0 {
		r.Status = "withheld"
		r.Reason = insp.Withheld.Reason
		if r.Reason == "" {
			r.Reason = "no_written_text"
		}
		return r
	}
	r.Status, r.Reason = "shown", "none"
	if insp.Withheld.Reason != "" {
		r.Reason = insp.Withheld.Reason
	}
	return r
}

// migrateWithReader is migrateDecision over the reader's Inspection and the
// typed built brief. It predicts the same result for the same folder.
func migrateWithReader(insp briefpkg.Inspection, built briefpkg.Document, builtOK bool, withdrawn []briefpkg.Withdrawn, now time.Time) string {
	if insp.Schema != sealedSchema || insp.Rule == sealRule || insp.Withheld.Reason != withheldSource {
		return "none"
	}
	if !builtOK || !recentDay(built.LocalDate, now) {
		return "none"
	}
	changed := map[string]bool{}
	for _, ref := range built.Evidence {
		for _, key := range withdrawn {
			if keyMatches(ref.SourceID, key.ID) {
				changed[ref.Ref] = true
			}
		}
	}
	for _, para := range built.Narrative {
		for _, ref := range para.Refs {
			if changed[ref] {
				return "withheld"
			}
		}
	}
	if len(built.Narrative) == 0 {
		return "withheld"
	}
	return "shown"
}
