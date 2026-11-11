package brief

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Decision is what a read of one brief decides, from the files alone. The names
// follow the seal v2 read in the app (mobile_brief_finished_read.go).
type Decision string

const (
	// DecisionNoCopy: there is no sealed copy. The app seals the brief on read.
	DecisionNoCopy Decision = "no_copy"
	// DecisionOlderSchema: the copy is a schema 1 file. The app seals it again.
	DecisionOlderSchema Decision = "older_schema"
	// DecisionUnreadable: the copy or the brief inside it cannot be read.
	DecisionUnreadable Decision = "unreadable"
	// DecisionShown: the copy was sealed under the current print and day.
	DecisionShown Decision = "shown"
	// DecisionReseal: the print matches but the local day changed. The copy is
	// shown and the app seals it again in the background.
	DecisionReseal Decision = "reseal"
	// DecisionAmendKeyed: the print changed only in the privacy epoch and the
	// withdrawal log names every change since the copy. The app drops what cites
	// a withdrawn source. The Reader does not amend, so it returns no content.
	DecisionAmendKeyed Decision = "amend_keyed"
	// DecisionAmendGates: the print changed in another input, or the log cannot
	// say what changed. The app runs the privacy gates again for this brief.
	DecisionAmendGates Decision = "amend_gates"
)

const (
	sealedSchema      = 2
	legacySchema      = 1
	copyExt           = ".read"
	builtExt          = ".json"
	latestFileName    = "latest.json"
	privacyEpochFile  = "privacy-epoch"
	withdrawalLogFile = "withdrawals.json"
	madeAheadReason   = "made_ahead"
	quietStatus       = "quiet"
	defaultListLimit  = 30
	maxListLimit      = 8192
)

// idPattern is the only shape a brief ID may take. It matches the app's
// mobileBriefIDPattern.
var idPattern = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}-(morning|evening|on_request-[0-9]{6})$`,
)

// ReaderOptions configures a Reader. Only Dir is required.
type ReaderOptions struct {
	// Dir is the briefs folder, <data>/mobile/briefs.
	Dir string
	// Print returns the current privacy print (the app's briefPrivacyPrint).
	// Nil means the print is unknown: only the saved privacy epoch is compared,
	// which is what a tool outside the app can know.
	Print func() string
	// Day returns the local day a copy must be sealed on. Nil skips the check.
	Day func() string
	// Now returns the time for held (made-ahead) briefs. Nil uses time.Now.
	Now func() time.Time
}

// Reader reads the briefs folder. It never writes, renames or removes a file,
// and it calls no live source. It is safe for concurrent use: each call reads
// the files it needs.
type Reader struct {
	opts ReaderOptions
}

// NewReader returns a Reader over opts.Dir.
func NewReader(opts ReaderOptions) *Reader {
	return &Reader{opts: opts}
}

// copyFile is the sealed copy as stored beside a brief (mobileBriefFinishedRead).
// Only the fields the read decision and the counts need are declared.
type copyFile struct {
	Schema         int             `json:"schema"`
	Rule           int             `json:"rule,omitempty"`
	Epoch          uint64          `json:"epoch,omitempty"`
	Print          string          `json:"print"`
	Day            string          `json:"day,omitempty"`
	WithheldReason string          `json:"withheld_reason,omitempty"`
	WithheldKind   string          `json:"withheld_kind,omitempty"`
	Brief          json.RawMessage `json:"brief"`
}

// logEntry and withdrawalLog mirror briefs/withdrawals.json.
type logEntry struct {
	Epoch uint64      `json:"epoch"`
	Keys  []Withdrawn `json:"keys,omitempty"`
}

type withdrawalLog struct {
	Floor   uint64     `json:"floor"`
	Entries []logEntry `json:"entries"`
}

// Summary is one row of List: the header of a stored brief, no words.
type Summary struct {
	BriefID         string
	Slot            string
	LocalDate       string
	AsOf            string
	NarrativeStatus string
}

// Inspection describes one stored brief for tools that compare readers. It
// carries counts and decisions only, never words.
type Inspection struct {
	ID       string
	Decision Decision
	Withheld Withholding
	// Schema and Rule are the copy's fields; zero when the copy is unreadable.
	Schema int
	Rule   int
	// Copy counts the sealed copy's brief; Built counts the stored .json.
	Copy    Counts
	Built   Counts
	BuiltOK bool
}

// Epoch returns the saved privacy epoch (the privacy-epoch file), 0 if missing.
func (r *Reader) Epoch() uint64 {
	data, err := os.ReadFile(filepath.Join(r.opts.Dir, privacyEpochFile))
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// Withdrawn returns every keyed withdrawal in the log, oldest first. A missing
// or unreadable log has none.
func (r *Reader) Withdrawn() []Withdrawn {
	var keys []Withdrawn
	for _, entry := range r.log(0).Entries {
		keys = append(keys, entry.Keys...)
	}
	return keys
}

// Get returns the sealed copy of one brief when it can be shown as it is or
// with a background reseal (DecisionShown or DecisionReseal). For any other
// decision it returns false, and the Sealed carries only the decision: the
// words stay out of the result, because the app must amend or gate them first.
func (r *Reader) Get(id string) (Sealed, bool) {
	sealed := r.seal(id)
	return sealed, sealed.Decision == DecisionShown || sealed.Decision == DecisionReseal
}

// Latest returns the sealed copy of the latest brief (see LatestBuilt).
func (r *Reader) Latest() (Sealed, bool) {
	doc, ok := r.LatestBuilt()
	if !ok {
		return Sealed{}, false
	}
	return r.Get(doc.BriefID)
}

// LatestBuilt names the latest brief the way the app's store does: the one
// latest.json points at when it is readable and not held, otherwise the newest
// stored brief. It reads the built briefs, not the sealed copies.
func (r *Reader) LatestBuilt() (Document, bool) {
	if data, err := os.ReadFile(filepath.Join(r.opts.Dir, latestFileName)); err == nil {
		var pointer struct {
			BriefID string `json:"briefId"`
		}
		if json.Unmarshal(data, &pointer) == nil {
			if doc, ok := r.built(pointer.BriefID); ok && !r.held(doc) {
				return doc, true
			}
		}
	}
	briefs := r.builtBriefs()
	if len(briefs) == 0 {
		return Document{}, false
	}
	return briefs[0], true
}

// List returns the newest stored briefs first, skipping quiet ones, like the
// app's store list. A limit of zero or less means the default of 30.
func (r *Reader) List(limit int) []Summary {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	out := []Summary{}
	for _, doc := range r.builtBriefs() {
		if doc.NarrativeStatus == quietStatus {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, Summary{
			BriefID:         doc.BriefID,
			Slot:            doc.Slot,
			LocalDate:       doc.LocalDate,
			AsOf:            doc.AsOf,
			NarrativeStatus: doc.NarrativeStatus,
		})
	}
	return out
}

// Built returns the stored brief (the .json the copy was made from). It is a
// read of the built file, not of the sealed copy, so callers must not show it.
func (r *Reader) Built(id string) (Document, bool) {
	return r.built(id)
}

// Inspect reports the decision and the counts of one stored brief, for tools
// that compare readers. It reads the sealed copy and the built brief, and it
// reports counts only.
func (r *Reader) Inspect(id string) Inspection {
	out := Inspection{ID: id}
	if !validID(id) {
		out.Decision = DecisionUnreadable
		return out
	}
	built, builtOK := r.built(id)
	out.BuiltOK = builtOK
	if builtOK {
		out.Built = countsOf(built)
	}
	saved, decision, ok := r.loadCopy(id)
	if saved.Schema != 0 {
		out.Schema, out.Rule = saved.Schema, saved.Rule
	}
	if !ok {
		out.Decision = decision
		return out
	}
	var doc Document
	if json.Unmarshal(saved.Brief, &doc) == nil {
		out.Copy = countsOf(doc)
	}
	out.Withheld = Withholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}
	out.Decision = r.decide(saved)
	return out
}

// seal decides one brief and returns its content only when it may be shown.
func (r *Reader) seal(id string) Sealed {
	if !validID(id) {
		return Sealed{Decision: DecisionUnreadable}
	}
	saved, decision, ok := r.loadCopy(id)
	if !ok {
		return Sealed{Decision: decision}
	}
	decision = r.decide(saved)
	sealed := Sealed{Decision: decision}
	if decision != DecisionShown && decision != DecisionReseal {
		return sealed
	}
	var doc Document
	if json.Unmarshal(saved.Brief, &doc) != nil || doc.BriefID != id {
		return Sealed{Decision: DecisionUnreadable}
	}
	sealed.Document = doc
	sealed.Raw = saved.Brief
	sealed.Counts = countsOf(doc)
	sealed.Withheld = Withholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}
	return sealed
}

// loadCopy reads the sealed copy file. The decision is set when the copy cannot
// be used (no file, an older schema, or unreadable content). The file is read
// only: an unreadable copy is left for the app to remove.
func (r *Reader) loadCopy(id string) (copyFile, Decision, bool) {
	data, err := os.ReadFile(filepath.Join(r.opts.Dir, id+copyExt))
	if err != nil {
		if os.IsNotExist(err) {
			return copyFile{}, DecisionNoCopy, false
		}
		return copyFile{}, DecisionUnreadable, false
	}
	var saved copyFile
	if json.Unmarshal(data, &saved) != nil {
		return copyFile{}, DecisionUnreadable, false
	}
	switch saved.Schema {
	case legacySchema:
		return saved, DecisionOlderSchema, false
	case sealedSchema:
	default:
		return saved, DecisionUnreadable, false
	}
	var doc Document
	if json.Unmarshal(saved.Brief, &doc) != nil || doc.BriefID != id {
		return saved, DecisionUnreadable, false
	}
	return saved, "", true
}

// decide is the seal v2 read decision for a loaded copy. It compares the print
// the copy was sealed under with the current one, and the day, and it reads the
// withdrawal log only when the print changed.
func (r *Reader) decide(saved copyFile) Decision {
	epoch := r.Epoch()
	current, day, dayChecked := r.current(saved, epoch)
	if saved.Print == current {
		if dayChecked && saved.Day != day {
			return DecisionReseal
		}
		return DecisionShown
	}
	if printMovedOnlyInEpoch(saved.Print, current) {
		if _, ok := withdrawnSince(r.log(epoch), saved.Epoch); ok {
			return DecisionAmendKeyed
		}
	}
	return DecisionAmendGates
}

// current returns the print, and the day, to compare a copy with. With no
// Print function, the print is the saved epoch with the copy's own other
// fields, so only a changed epoch counts as a change.
func (r *Reader) current(saved copyFile, epoch uint64) (string, string, bool) {
	day, dayChecked := "", false
	if r.opts.Day != nil {
		day, dayChecked = r.opts.Day(), true
	}
	if r.opts.Print != nil {
		return r.opts.Print(), day, dayChecked
	}
	rest := ""
	if i := strings.Index(saved.Print, "|"); i >= 0 {
		rest = saved.Print[i:]
	}
	return strconv.FormatUint(epoch, 36) + rest, day, dayChecked
}

// log reads the withdrawal log the way the app loads it. A missing or
// unreadable log starts at the epoch, and an epoch newer than the log's last
// entry means a change was saved without its entry, so the log starts over.
func (r *Reader) log(epoch uint64) withdrawalLog {
	if epoch == 0 {
		epoch = r.Epoch()
	}
	log := withdrawalLog{Floor: epoch}
	if data, err := os.ReadFile(filepath.Join(r.opts.Dir, withdrawalLogFile)); err == nil {
		var saved withdrawalLog
		if json.Unmarshal(data, &saved) == nil {
			log = saved
		}
	}
	last := log.Floor
	if n := len(log.Entries); n > 0 {
		last = log.Entries[n-1].Epoch
	}
	if epoch > last {
		log = withdrawalLog{Floor: epoch}
	}
	return log
}

// withdrawnSince returns the sources withdrawn after epoch. It reports false
// when the log cannot answer: the copy is older than the log, or a change in
// between named no sources.
func withdrawnSince(log withdrawalLog, epoch uint64) ([]Withdrawn, bool) {
	if epoch < log.Floor {
		return nil, false
	}
	var keys []Withdrawn
	for _, entry := range log.Entries {
		if entry.Epoch <= epoch {
			continue
		}
		if len(entry.Keys) == 0 {
			return nil, false
		}
		keys = append(keys, entry.Keys...)
	}
	return keys, true
}

// printMovedOnlyInEpoch reports whether the only part of the print that changed
// is the epoch. A print is epoch|enabled|paused|apps.
func printMovedOnlyInEpoch(saved, current string) bool {
	was, now := strings.Split(saved, "|"), strings.Split(current, "|")
	if len(was) != 4 || len(now) != 4 {
		return false
	}
	return was[1] == now[1] && was[2] == now[2] && was[3] == now[3]
}

// built reads a stored brief's .json. It must describe the same ID.
func (r *Reader) built(id string) (Document, bool) {
	if !validID(id) {
		return Document{}, false
	}
	data, err := os.ReadFile(filepath.Join(r.opts.Dir, id+builtExt))
	if err != nil {
		return Document{}, false
	}
	var doc Document
	if json.Unmarshal(data, &doc) != nil || doc.BriefID != id {
		return Document{}, false
	}
	return doc, true
}

// builtBriefs loads every valid stored brief that is not held, newest first.
func (r *Reader) builtBriefs() []Document {
	entries, err := os.ReadDir(r.opts.Dir)
	if err != nil {
		return nil
	}
	var out []Document
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), builtExt) {
			continue
		}
		doc, ok := r.built(strings.TrimSuffix(entry.Name(), builtExt))
		if ok && !r.held(doc) {
			out = append(out, doc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := asOfTime(out[i]), asOfTime(out[j])
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		return out[i].BriefID > out[j].BriefID
	})
	return out
}

// held reports whether an early brief must stay hidden: it is made ahead, and
// its slot has not come yet.
func (r *Reader) held(doc Document) bool {
	if doc.Generation == nil || doc.Generation.Reason != madeAheadReason {
		return false
	}
	at, err := time.Parse(time.RFC3339, doc.Generation.ScheduledAt)
	if err != nil {
		return false
	}
	now := time.Now()
	if r.opts.Now != nil {
		now = r.opts.Now()
	}
	return now.Before(at)
}

func asOfTime(doc Document) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, doc.AsOf)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func countsOf(doc Document) Counts {
	return Counts{
		Narrative: len(doc.Narrative),
		Cards:     len(doc.Cards),
		Evidence:  len(doc.Evidence),
		Audio:     len(doc.Audio) > 0 && string(doc.Audio) != "null",
	}
}

func validID(id string) bool {
	return idPattern.MatchString(id)
}
