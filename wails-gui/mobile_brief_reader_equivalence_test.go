package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	briefpkg "wails-gui/internal/brief"
)

// These tests prove that the read-only Reader in internal/brief decides and
// counts the same as the app's brief read path, on the brief fixtures under
// testdata and on variants of them. The app side runs the store and the read
// helpers of this package; the Reader side runs internal/brief. Each case
// writes its own folder, because an unreadable copy is removed by the app.

// briefReaderPrint formats a privacy print the way briefPrivacyPrint does:
// epoch|enabled|paused|apps, with the epoch in base 36.
func briefReaderPrint(epoch uint64, enabled, paused bool, apps string) string {
	return strconv.FormatUint(epoch, 36) + "|" + strconv.FormatBool(enabled) + "|" +
		strconv.FormatBool(paused) + "|" + apps
}

// setBriefReaderPrivacyState sets the in-memory privacy epoch and withdrawal log
// the app would hold, and restores them when the test ends.
func setBriefReaderPrivacyState(t *testing.T, epoch uint64, log briefWithdrawalLog) {
	t.Helper()
	prevEpoch := mobileBriefPrivacyEpoch.Load()
	mobileBriefPrivacyEpochMu.Lock()
	prevLog := mobileBriefWithdrawals
	mobileBriefWithdrawals = log
	mobileBriefPrivacyEpochMu.Unlock()
	mobileBriefPrivacyEpoch.Store(epoch)
	t.Cleanup(func() {
		mobileBriefPrivacyEpoch.Store(prevEpoch)
		mobileBriefPrivacyEpochMu.Lock()
		mobileBriefWithdrawals = prevLog
		mobileBriefPrivacyEpochMu.Unlock()
	})
}

// briefReaderFixtures returns the golden brief fixtures and variants of them:
// a brief with words, cards and audio; a quiet brief; and a brief held until a
// future slot. Each keeps the fixture's evidence rows.
func briefReaderFixtures(t *testing.T) []MobileBriefV1 {
	t.Helper()
	var golden []MobileBriefV1
	for _, name := range []string{"mobile_brief_golden_v1.json", "mobile_brief_golden_early_v1.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var brief MobileBriefV1
		if err := json.Unmarshal(data, &brief); err != nil {
			t.Fatal(err)
		}
		golden = append(golden, brief)
	}
	base := golden[0]
	rich := briefReaderVariant(t, base, map[string]any{
		"briefId": "2026-10-11-evening", "slot": "evening", "localDate": "2026-10-11",
		"asOf": "2026-10-11T19:00:00Z",
	}, func(doc map[string]any) {
		ref0, ref1 := briefReaderEvidenceRef(doc, 0), briefReaderEvidenceRef(doc, 1)
		doc["narrative"] = []any{
			map[string]any{"text": "First paragraph.", "refs": []any{ref0}},
			map[string]any{"text": "Second paragraph.", "refs": []any{ref1}},
		}
		doc["cards"] = []any{map[string]any{"title": "Card one", "refs": []any{ref0}}}
		doc["audio"] = map[string]any{"status": "ready", "mime": "audio/mp4"}
	})
	quiet := briefReaderVariant(t, base, map[string]any{
		"briefId": "2026-10-12-morning", "localDate": "2026-10-12", "asOf": "2026-10-12T14:00:00Z",
		"narrativeStatus": "quiet",
	}, nil)
	held := briefReaderVariant(t, rich, map[string]any{
		"briefId": "2026-10-13-morning", "localDate": "2026-10-13", "asOf": "2026-10-13T08:00:00Z",
		"slot": "morning",
	}, func(doc map[string]any) {
		doc["generation"] = map[string]any{
			"reason": "made_ahead", "scheduledAt": "2099-01-01T08:00:00Z",
			"generatedAt": "2026-10-12T20:00:00Z", "message": "",
		}
	})
	return append(golden, rich, quiet, held)
}

// briefReaderVariant returns base with the given top-level fields replaced and
// mutate applied, round-tripped through the wire JSON.
func briefReaderVariant(t *testing.T, base MobileBriefV1, set map[string]any, mutate func(map[string]any)) MobileBriefV1 {
	t.Helper()
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for key, value := range set {
		doc[key] = value
	}
	if mutate != nil {
		mutate(doc)
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out MobileBriefV1
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func briefReaderEvidenceRef(doc map[string]any, index int) string {
	evidence, _ := doc["evidence"].([]any)
	row, _ := evidence[index].(map[string]any)
	ref, _ := row["ref"].(string)
	return ref
}

// briefReaderCase is one sealed-copy state to build around a fixture.
type briefReaderCase struct {
	name string
	// copyPrint, copyEpoch and copyDay are what the copy was sealed under.
	copyPrint string
	copyEpoch uint64
	copyDay   string
	// current print, epoch and day, as the app holds them at read time.
	print string
	epoch uint64
	day   string
	log   briefWithdrawalLog
	// copyKind is "sealed", "schema1", "garbage" or "none".
	copyKind string
	// withheld is the copy's withheld_reason, if any.
	withheld string
}

func briefReaderCases(brief MobileBriefV1) []briefReaderCase {
	day := brief.LocalDate
	ev0 := ""
	if len(brief.Evidence) > 0 {
		ev0 = brief.Evidence[0].SourceID
	}
	p5 := briefReaderPrint(5, false, false, "apps")
	p6 := briefReaderPrint(6, false, false, "apps")
	return []briefReaderCase{
		{name: "shown", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p5, epoch: 5, day: day,
			log: briefWithdrawalLog{Floor: 5}, copyKind: "sealed"},
		{name: "reseal_new_day", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p5, epoch: 5, day: "2026-12-31",
			log: briefWithdrawalLog{Floor: 5}, copyKind: "sealed"},
		{name: "amend_keyed", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p6, epoch: 6, day: day,
			log: briefWithdrawalLog{Floor: 5, Entries: []briefWithdrawal{{Epoch: 6, Keys: []briefWithdrawn{{ID: ev0}}}}}, copyKind: "sealed"},
		{name: "amend_gates_unnamed", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p6, epoch: 6, day: day,
			log: briefWithdrawalLog{Floor: 5, Entries: []briefWithdrawal{{Epoch: 6}}}, copyKind: "sealed"},
		{name: "amend_gates_log_behind", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p6, epoch: 6, day: day,
			log: briefWithdrawalLog{Floor: 6}, copyKind: "sealed"},
		{name: "amend_gates_pause", copyPrint: p5, copyEpoch: 5, copyDay: day,
			print: briefReaderPrint(5, true, false, "apps"), epoch: 5, day: day,
			log: briefWithdrawalLog{Floor: 5}, copyKind: "sealed"},
		{name: "withheld_passthrough", copyPrint: p5, copyEpoch: 5, copyDay: day, print: p5, epoch: 5, day: day,
			log: briefWithdrawalLog{Floor: 5}, copyKind: "sealed", withheld: "source_withdrawn"},
		{name: "no_copy", print: p5, epoch: 5, day: day, log: briefWithdrawalLog{Floor: 5}, copyKind: "none"},
		{name: "older_schema", print: p5, epoch: 5, day: day, log: briefWithdrawalLog{Floor: 5}, copyKind: "schema1"},
		{name: "unreadable", print: p5, epoch: 5, day: day, log: briefWithdrawalLog{Floor: 5}, copyKind: "garbage"},
	}
}

// writeBriefReaderFolder writes one brief and its sealed copy (per the case)
// into dir, with the privacy epoch and withdrawal log files the app keeps.
func writeBriefReaderFolder(t *testing.T, dir string, brief MobileBriefV1, c briefReaderCase) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	built, err := marshalStoredMobileBrief(brief)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, brief.BriefID+".json"), built, 0600); err != nil {
		t.Fatal(err)
	}
	store := &mobileBriefStore{dir: dir}
	switch c.copyKind {
	case "sealed":
		sealedBrief := brief
		if c.withheld != "" {
			sealedBrief.Narrative = nil
		}
		raw, err := marshalStoredMobileBrief(sealedBrief)
		if err != nil {
			t.Fatal(err)
		}
		read := mobileBriefFinishedRead{
			Schema: mobileBriefSealedSchema, Rule: mobileBriefSealRule, Epoch: c.copyEpoch,
			Print: c.copyPrint, Source: mobileBriefSourceDigest(brief), Day: c.copyDay,
			SourceRefs: []mobileBriefSealSource{}, WithheldReason: c.withheld, Brief: raw,
		}
		if c.withheld != "" {
			read.WithheldKind = "principal"
		}
		if err := store.saveFinishedRead(brief.BriefID, read); err != nil {
			t.Fatal(err)
		}
	case "schema1":
		writeBriefReaderFile(t, filepath.Join(dir, brief.BriefID+copyExtForTest), `{"schema":1,"key":"old","brief":{}}`)
	case "garbage":
		writeBriefReaderFile(t, filepath.Join(dir, brief.BriefID+copyExtForTest), "not json at all")
	}
	writeBriefReaderFile(t, filepath.Join(dir, mobileBriefPrivacyEpochFile), strconv.FormatUint(c.epoch, 10))
	log, err := json.Marshal(c.log)
	if err != nil {
		t.Fatal(err)
	}
	writeBriefReaderFile(t, filepath.Join(dir, mobileBriefWithdrawalLogFile), string(log))
}

const copyExtForTest = ".read"

func writeBriefReaderFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// appBriefRead mirrors the decision of finishedReadWithholding (the app's
// read of a sealed copy) with the store and read helpers this package runs.
// It runs the same branches in the same order and reports the counts of the
// words the app would show.
func appBriefRead(t *testing.T, dir, id, print, day string) (string, briefpkg.Counts, briefpkg.Withholding) {
	t.Helper()
	store := &mobileBriefStore{dir: dir}
	saved, found, err := store.loadFinishedRead(id)
	switch {
	case err != nil:
		return string(briefpkg.DecisionUnreadable), briefpkg.Counts{}, briefpkg.Withholding{}
	case !found:
		return string(briefpkg.DecisionNoCopy), briefpkg.Counts{}, briefpkg.Withholding{}
	case saved.Schema != mobileBriefSealedSchema:
		return string(briefpkg.DecisionOlderSchema), briefpkg.Counts{}, briefpkg.Withholding{}
	}
	var brief MobileBriefV1
	if unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != id {
		return string(briefpkg.DecisionUnreadable), briefpkg.Counts{}, briefpkg.Withholding{}
	}
	withheld := briefpkg.Withholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}
	counts := briefpkg.Counts{
		Narrative: len(brief.Narrative), Cards: len(brief.Cards),
		Evidence: len(brief.Evidence), Audio: brief.Audio != nil,
	}
	if saved.Print != print {
		if briefPrintMovedOnlyInEpoch(saved.Print, print) {
			if _, ok := withdrawnSince(saved.Epoch); ok {
				return string(briefpkg.DecisionAmendKeyed), briefpkg.Counts{}, briefpkg.Withholding{}
			}
		}
		return string(briefpkg.DecisionAmendGates), briefpkg.Counts{}, briefpkg.Withholding{}
	}
	if saved.Day != day {
		return string(briefpkg.DecisionReseal), counts, withheld
	}
	return string(briefpkg.DecisionShown), counts, withheld
}

// TestBriefReaderEquivalenceOnFixtures runs every fixture through every sealed
// state. For each, the app path and the Reader must give the same decision, the
// same Get result, and, when words are shown, the same counts and withholding.
func TestBriefReaderEquivalenceOnFixtures(t *testing.T) {
	wordsCompared := 0
	for _, brief := range briefReaderFixtures(t) {
		for _, c := range briefReaderCases(brief) {
			t.Run(brief.BriefID+"/"+c.name, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "briefs")
				writeBriefReaderFolder(t, dir, brief, c)
				setBriefReaderPrivacyState(t, c.epoch, c.log)

				reader := briefpkg.NewReader(briefpkg.ReaderOptions{
					Dir:   dir,
					Print: func() string { return c.print },
					Day:   func() string { return c.day },
					Now:   time.Now,
				})
				sealed, ok := reader.Get(brief.BriefID)
				gotDecision := string(sealed.Decision)

				wantDecision, wantCounts, wantWithheld := appBriefRead(t, dir, brief.BriefID, c.print, c.day)
				if gotDecision != wantDecision {
					t.Fatalf("decision: reader %q, app %q", gotDecision, wantDecision)
				}
				wantOK := wantDecision == string(briefpkg.DecisionShown) || wantDecision == string(briefpkg.DecisionReseal)
				if ok != wantOK {
					t.Fatalf("Get ok: reader %t, app shows %t", ok, wantOK)
				}
				if !ok {
					if sealed.Raw != nil || sealed.Document.BriefID != "" {
						t.Fatalf("Get returned content for decision %q", gotDecision)
					}
					return
				}
				if sealed.Counts != wantCounts {
					t.Fatalf("counts: reader %+v, app %+v", sealed.Counts, wantCounts)
				}
				if sealed.Withheld != wantWithheld {
					t.Fatalf("withholding: reader %+v, app %+v", sealed.Withheld, wantWithheld)
				}
				if sealed.Counts.Narrative > 0 && sealed.Counts.Audio && sealed.Counts.Cards > 0 {
					wordsCompared++
				}
			})
		}
	}
	// Guard against a vacuous pass: the rich fixture must reach the shown path
	// with words, cards and audio on both sides of the comparison.
	if wordsCompared == 0 {
		t.Fatal("no shown case compared words, cards and audio; the fixtures do not exercise the read")
	}
}

// TestBriefReaderEquivalenceLatestAndList runs the store's latest and list
// against the Reader on one folder with every fixture, including a quiet brief
// and a held brief, with the pointer on a readable brief and on a held one.
func TestBriefReaderEquivalenceLatestAndList(t *testing.T) {
	fixtures := briefReaderFixtures(t)
	for _, pointer := range []string{"2026-10-11-evening", "2026-10-13-morning"} {
		t.Run("pointer="+pointer, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "briefs")
			day := fixtures[2].LocalDate
			c := briefReaderCase{copyPrint: briefReaderPrint(5, false, false, "apps"), copyEpoch: 5, copyDay: day,
				print: briefReaderPrint(5, false, false, "apps"), epoch: 5, day: day,
				log: briefWithdrawalLog{Floor: 5}, copyKind: "sealed"}
			setBriefReaderPrivacyState(t, c.epoch, c.log)
			for _, brief := range fixtures {
				writeBriefReaderFolder(t, dir, brief, c)
			}
			pointerJSON := fmt.Sprintf(`{"version":1,"briefId":%q}`, pointer)
			writeBriefReaderFile(t, filepath.Join(dir, mobileBriefLatestFileName), pointerJSON)

			store := &mobileBriefStore{dir: dir}
			reader := briefpkg.NewReader(briefpkg.ReaderOptions{Dir: dir, Print: func() string { return c.print }, Day: func() string { return day }})

			appLatest, appOK := store.latest()
			gotLatest, gotOK := reader.LatestBuilt()
			if appOK != gotOK || appLatest.BriefID != gotLatest.BriefID {
				t.Fatalf("latest: reader %q (%t), app %q (%t)", gotLatest.BriefID, gotOK, appLatest.BriefID, appOK)
			}
			if len(appLatest.Narrative) != len(gotLatest.Narrative) || len(appLatest.Evidence) != len(gotLatest.Evidence) {
				t.Fatalf("latest counts differ")
			}

			for _, limit := range []int{0, 2} {
				var appIDs []string
				for _, row := range store.list(limit) {
					appIDs = append(appIDs, row.BriefID)
				}
				var gotIDs []string
				for _, row := range reader.List(limit) {
					gotIDs = append(gotIDs, row.BriefID)
				}
				if !reflect.DeepEqual(appIDs, gotIDs) {
					t.Fatalf("list(%d): reader %v, app %v", limit, gotIDs, appIDs)
				}
			}
		})
	}
}

// TestBriefDocumentWireTagsMatchMobileBriefV1 keeps the reader's document types
// on the wire names of the app's types, so the JSON stays identical.
func TestBriefDocumentWireTagsMatchMobileBriefV1(t *testing.T) {
	pairs := []struct {
		name    string
		reader  reflect.Type
		appWire reflect.Type
	}{
		{"Document", reflect.TypeOf(briefpkg.Document{}), reflect.TypeOf(MobileBriefV1{})},
		{"Paragraph", reflect.TypeOf(briefpkg.Paragraph{}), reflect.TypeOf(MobileBriefPara{})},
		{"Evidence", reflect.TypeOf(briefpkg.Evidence{}), reflect.TypeOf(MobileBriefRef{})},
		{"Generation", reflect.TypeOf(briefpkg.Generation{}), reflect.TypeOf(MobileBriefGeneration{})},
		{"RequestResult", reflect.TypeOf(briefpkg.RequestResult{}), reflect.TypeOf(MobileBriefRequestResult{})},
		{"Withdrawn", reflect.TypeOf(briefpkg.Withdrawn{}), reflect.TypeOf(briefWithdrawn{})},
		{"PrivacyChange", reflect.TypeOf(briefpkg.PrivacyChange{}), reflect.TypeOf(briefWithdrawal{})},
	}
	for _, pair := range pairs {
		names := map[string]bool{}
		for i := 0; i < pair.appWire.NumField(); i++ {
			if name := jsonTagName(pair.appWire.Field(i)); name != "" {
				names[name] = true
			}
		}
		for i := 0; i < pair.reader.NumField(); i++ {
			name := jsonTagName(pair.reader.Field(i))
			if name == "" {
				continue
			}
			if !names[name] {
				t.Errorf("%s: json field %q is not in the app's wire type %s", pair.name, name, pair.appWire.Name())
			}
		}
	}
}

func jsonTagName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}
