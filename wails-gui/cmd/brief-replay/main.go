// Command brief-replay runs the sealed-brief read decision over every stored
// brief in a data folder and prints one count-only line per brief. It is
// read-only: it opens files for reading and writes nothing, not even a copy.
//
// Output is one line per brief: the brief id, shown or withheld, the reason,
// the source kind, the counts of the sealed copy, the counts of the built brief
// (the stored .json the copy was made from), and what the one-time migration
// would do to the copy. It never prints text, titles, names, or paths.
//
// The decision mirrors the app's read (mobile_brief_finished_read.go). A copy
// is shown while its privacy epoch matches the epoch file. The privacy print
// also holds the Closed Eyes, pause and sensitive-app state, which this tool
// cannot evaluate without the running app, so a stale print is reported as
// "amend" (the app would re-check it before showing it) and nothing is hidden
// for those inputs.
//
// The migration column (migrate=shown|withheld|none) says what the start-up
// migration would do to a copy the old edited-source rule hid (schema 2, no
// rule field, withheld_reason=source_withdrawn). The privacy gates need the
// running app, so this tool does not run them: a migrate=shown line is a
// prediction from the built brief and the recorded keyed withdrawals only. The
// app's gates may still withhold words that cite a protected source.
//
// Usage:
//
//	go run ./cmd/brief-replay -h
//	go run ./cmd/brief-replay                 # default: the app data folder
//	go run ./cmd/brief-replay -data /path/to/data-root
//	go run ./cmd/brief-replay -reader=new    # the decision and counts from internal/brief
//	go run ./cmd/brief-replay -shadow        # also compare each brief with its shadow copy (counts only)
//
// -reader=new prints the same lines as the default, so the two runs can be
// compared on the same folder. The briefs are read from <data>/mobile/briefs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/apphome"
)

// These names mirror the app's brief store (mobile_brief_store.go,
// mobile_brief_finished_read.go and mobile_brief_migrate.go). They are duplicated
// because this command is a separate package and must not import the app.
const (
	mobileDirName     = "mobile"
	briefDirName      = "briefs"
	finishedReadExt   = ".read"
	sealedSchema      = 2
	sealRule          = 2
	withheldSource    = "source_withdrawn"
	privacyEpochFile  = "privacy-epoch"
	withdrawalLogFile = "withdrawals.json"
	briefIDPatternRaw = `^[0-9]{4}-[0-9]{2}-[0-9]{2}-(morning|evening|on_request-[0-9]{6})$`
)

var briefIDPattern = regexp.MustCompile(briefIDPatternRaw)

// copyFile mirrors the fields of the sealed copy that this tool reads.
type copyFile struct {
	Schema         int             `json:"schema"`
	Rule           int             `json:"rule,omitempty"`
	Print          string          `json:"print"`
	WithheldReason string          `json:"withheld_reason,omitempty"`
	WithheldKind   string          `json:"withheld_kind,omitempty"`
	Brief          json.RawMessage `json:"brief"`
}

// builtBrief mirrors the parts of the built brief (the stored .json) that this
// tool counts and matches against withdrawn keys. Only counts leave this tool.
type builtBrief struct {
	BriefID   string `json:"briefId"`
	LocalDate string `json:"localDate"`
	Narrative []struct {
		Refs []string `json:"refs"`
	} `json:"narrative"`
	Cards    []json.RawMessage `json:"cards"`
	Evidence []struct {
		Ref      string `json:"ref"`
		SourceID string `json:"sourceId"`
	} `json:"evidence"`
	Audio json.RawMessage `json:"audio"`
}

// withdrawalKey and withdrawalLog mirror the keyed withdrawal log
// (briefs/withdrawals.json).
type withdrawalKey struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id"`
}

type withdrawalLog struct {
	Floor   uint64 `json:"floor"`
	Entries []struct {
		Epoch uint64          `json:"epoch"`
		Keys  []withdrawalKey `json:"keys,omitempty"`
	} `json:"entries"`
}

type result struct {
	ID     string
	Status string
	Reason string
	Kind   string
	Counts [3]int
	Audio  bool
	Built  [3]int
	// BuiltOK is false when the built brief cannot be read.
	BuiltOK bool
	Migrate string
}

func main() {
	data := flag.String("data", "", "app data root, the folder that holds mobile/ (default: the Isoform data folder, resolved at runtime)")
	shadow := flag.Bool("shadow", false, "also compare each stored brief with its shadow copy (briefs/shadow/<id>.json): counts and section counts only")
	readerMode := flag.String("reader", "old", "old: this command's own decision; new: the decision and counts from internal/brief (same output format, for comparing the two)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: brief-replay [-data DIR] [-reader old|new] [-shadow]\n\nPrints one count-only line per stored brief. Read-only. The briefs are read from DIR/mobile/briefs.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	root := *data
	if root == "" {
		dir, err := apphome.DeveloperDir()
		if err != nil {
			log.Fatal(err)
		}
		root = dir
	}
	fmt.Println("gates=skipped: the privacy gates need the running app; migrate is predicted from the built brief and the keyed withdrawal log")
	var results []result
	var err error
	switch *readerMode {
	case "old":
		results, err = replay(briefsDirFor(root))
	case "new":
		results, err = replayWithReader(briefsDirFor(root))
	default:
		log.Fatalf("unknown -reader %q: use old or new", *readerMode)
	}
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range results {
		fmt.Printf("%s %s reason=%s kind=%s narrative=%d cards=%d evidence=%d audio=%t built_narrative=%d built_cards=%d built_evidence=%d built=%t migrate=%s\n",
			r.ID, r.Status, r.Reason, r.Kind, r.Counts[0], r.Counts[1], r.Counts[2], r.Audio,
			r.Built[0], r.Built[1], r.Built[2], r.BuiltOK, r.Migrate)
	}
	fmt.Printf("briefs=%d\n", len(results))
	if *shadow {
		shadowReport(briefsDirFor(root), results)
	}
}

// briefsDirFor returns where the briefs of an app data root are stored.
func briefsDirFor(root string) string {
	return filepath.Join(root, mobileDirName, briefDirName)
}

// replay decides every stored brief under dir. It reads only.
func replay(dir string) ([]result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	epoch := readEpoch(filepath.Join(dir, privacyEpochFile))
	withdrawn := readWithdrawnKeys(filepath.Join(dir, withdrawalLogFile))
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
		out = append(out, decide(dir, id, epoch, withdrawn))
	}
	return out, nil
}

// decide is the read decision for one stored brief, and what the migration
// would do to its copy.
func decide(dir, id string, epoch uint64, withdrawn []withdrawalKey) result {
	r := result{ID: id, Migrate: "none"}
	built, builtOK := readBuilt(filepath.Join(dir, id+".json"), id)
	r.BuiltOK = builtOK
	if builtOK {
		r.Built = [3]int{len(built.Narrative), len(built.Cards), len(built.Evidence)}
	}
	data, err := os.ReadFile(filepath.Join(dir, id+finishedReadExt))
	if err != nil {
		r.Status, r.Reason = "withheld", "no_copy"
		return r
	}
	var copied copyFile
	if err := json.Unmarshal(data, &copied); err != nil || copied.Schema != sealedSchema {
		r.Status, r.Reason = "withheld", "unreadable_copy"
		return r
	}
	var shape builtBrief
	if err := json.Unmarshal(copied.Brief, &shape); err != nil || shape.BriefID != id {
		r.Status, r.Reason = "withheld", "unreadable_copy"
		return r
	}
	r.Counts = [3]int{len(shape.Narrative), len(shape.Cards), len(shape.Evidence)}
	r.Audio = len(shape.Audio) > 0 && string(shape.Audio) != "null"
	r.Kind = copied.WithheldKind
	r.Migrate = migrateDecision(copied, built, builtOK, withdrawn, time.Now())
	if !printEpochMatches(copied.Print, epoch) {
		r.Status, r.Reason = "amend", "privacy_epoch_changed"
		return r
	}
	if len(shape.Narrative) == 0 {
		r.Status = "withheld"
		r.Reason = copied.WithheldReason
		if r.Reason == "" {
			r.Reason = "no_written_text"
		}
		return r
	}
	r.Status, r.Reason = "shown", "none"
	if copied.WithheldReason != "" {
		r.Reason = copied.WithheldReason
	}
	return r
}

// migrateDecision predicts the one-time migration of a copy the old edited-source
// rule hid. The migration seals the built brief again and drops what cites a
// recorded keyed withdrawal; a written synthesis with any paragraph citing a
// withdrawn source is dropped whole. "none" when the copy is not a candidate or
// the built brief is missing.
func migrateDecision(copied copyFile, built builtBrief, builtOK bool, withdrawn []withdrawalKey, now time.Time) string {
	if copied.Schema != sealedSchema || copied.Rule == sealRule || copied.WithheldReason != withheldSource {
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

// migrationMaxAgeDays mirrors the app's limit (mobile_brief_migrate.go): a brief
// is migrated only when its local day is today or within the two days before.
const migrationMaxAgeDays = 2

// recentDay reports whether a brief's local day (YYYY-MM-DD) is within the limit.
func recentDay(localDate string, now time.Time) bool {
	day, err := time.Parse("2006-01-02", localDate)
	if err != nil {
		return false
	}
	today, err := time.Parse("2006-01-02", now.Format("2006-01-02"))
	if err != nil {
		return false
	}
	age := int(today.Sub(day).Hours() / 24)
	return age >= 0 && age <= migrationMaxAgeDays
}

// keyMatches mirrors briefRefWithdrawn: a match is by id, whole or after a prefix.
func keyMatches(id, withdrawnID string) bool {
	if id == "" || withdrawnID == "" {
		return false
	}
	return id == withdrawnID || strings.HasSuffix(withdrawnID, ":"+id) || strings.HasSuffix(id, ":"+withdrawnID)
}

// readBuilt reads the built brief. It never writes the file.
func readBuilt(path, id string) (builtBrief, bool) {
	var built builtBrief
	data, err := os.ReadFile(path)
	if err != nil {
		return built, false
	}
	if err := json.Unmarshal(data, &built); err != nil || built.BriefID != id {
		return built, false
	}
	return built, true
}

// readWithdrawnKeys returns every keyed withdrawal in the log. A missing or
// unreadable log has none. It never writes the file.
func readWithdrawnKeys(path string) []withdrawalKey {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var log withdrawalLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil
	}
	var keys []withdrawalKey
	for _, entry := range log.Entries {
		keys = append(keys, entry.Keys...)
	}
	return keys
}

// printEpochMatches compares the epoch part of a sealed print (base 36, the
// first field) with the saved epoch (decimal). A missing epoch file counts as zero.
func printEpochMatches(print string, epoch uint64) bool {
	first, _, _ := strings.Cut(print, "|")
	sealed, err := strconv.ParseUint(first, 36, 64)
	if err != nil {
		return false
	}
	return sealed == epoch
}

// readEpoch reads the saved privacy epoch. It never writes the file back.
func readEpoch(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}
