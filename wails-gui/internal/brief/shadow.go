package brief

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ShadowDeps is Deps plus the one check a shadow run needs before it reads.
type ShadowDeps interface {
	Deps
	// BudgetLow reports that the model budget cannot pay for a shadow run. The
	// app counts the shadow's own model calls against the same budget.
	BudgetLow() bool
}

// ShadowDirName is the folder under the briefs folder that holds shadow copies.
// Shadow mode writes nothing anywhere else.
const ShadowDirName = "shadow"

// ErrShadowID is returned for a brief ID that is not in the briefs' ID shape.
// It is the guard against a path that leaves the shadow folder.
var ErrShadowID = errors.New("brief shadow: brief id is not valid")

// ShadowOld are the old pipeline's counts for the brief a shadow copy is
// compared with. Sections holds the composed section names only.
type ShadowOld struct {
	Narrative int
	Cards     int
	Evidence  int
	Sections  map[string]int
}

// ShadowReport is what one shadow run found. It holds counts and section names
// only, so it can be logged.
type ShadowReport struct {
	ID           string
	Old          ShadowOld
	New          ShadowCounts
	Sections     map[string]int
	SectionsDiff []string
	Narrative    string
	Judge        string
	DoneParts    int
	NextParts    int
	JudgePass    bool
	Ms           int64
	Path         string
}

// partCounts returns how many paragraphs the done and next parts hold.
func partCounts(parts []NarrativePart) (done, next int) {
	for _, p := range parts {
		switch p.Kind {
		case PartDone:
			done += len(p.Paragraphs)
		case PartNext:
			next += len(p.Paragraphs)
		}
	}
	return done, next
}

// shadowFile is the on-disk shadow copy. The brief part holds the composed words
// for review in the app's data folder; it never leaves that folder.
type shadowFile struct {
	Schema    int            `json:"schema"`
	ID        string         `json:"id"`
	Narrative string         `json:"narrative_status"`
	Judge     string         `json:"judge"`
	Counts    ShadowCounts   `json:"counts"`
	Sections  map[string]int `json:"sections"`
	Ms        int64          `json:"ms"`
	Brief     Composed       `json:"brief"`
}

const shadowFileSchema = 1

// RunShadow gathers and composes the brief for id, then writes the result
// under <dir>/shadow/<id>.json. It never writes the brief itself and never
// reaches the phone. The old brief is only read by the caller, so a failure
// here cannot change it. It returns a report and logs one count-only line.
func RunShadow(ctx context.Context, deps ShadowDeps, dir, id string, w Window, old ShadowOld) (ShadowReport, error) {
	if !idPattern.MatchString(id) {
		return ShadowReport{}, ErrShadowID
	}
	if dir == "" {
		return ShadowReport{}, errors.New("brief shadow: no briefs folder")
	}
	start := time.Now()
	if deps.BudgetLow() {
		return ShadowReport{}, ErrBudgetLow
	}
	in, err := Gather(ctx, deps, w)
	if err != nil {
		return ShadowReport{}, err
	}
	c := Compose(in)
	narrative, judge, err := narrate(ctx, deps, in, &c)
	if err != nil {
		return ShadowReport{}, err
	}
	doneParts, nextParts := partCounts(c.NarrativeParts)
	report := ShadowReport{
		ID:        id,
		Old:       old,
		New:       c.Counts(),
		Sections:  c.SectionCounts(),
		Narrative: narrative,
		Judge:     judge,
		DoneParts: doneParts,
		NextParts: nextParts,
		JudgePass: judge == JudgePass,
		Ms:        time.Since(start).Milliseconds(),
	}
	report.SectionsDiff = sectionsDiff(old.Sections, report.Sections)
	file := shadowFile{
		Schema:    shadowFileSchema,
		ID:        id,
		Narrative: narrative,
		Judge:     judge,
		Counts:    report.New,
		Sections:  report.Sections,
		Ms:        report.Ms,
		Brief:     c,
	}
	path, err := writeShadowFile(dir, id, file)
	if err != nil {
		return ShadowReport{}, fmt.Errorf("brief shadow: write: %w", err)
	}
	report.Path = path
	LogShadow(report)
	return report, nil
}

// LogShadow writes the one count-only line for a shadow run. It never prints
// text, titles, names or paths.
func LogShadow(r ShadowReport) {
	diff := "none"
	if len(r.SectionsDiff) > 0 {
		diff = strings.Join(r.SectionsDiff, ",")
	}
	log.Printf("[brief-shadow] id=%s old_narrative=%d new_narrative=%d old_cards=%d new_cards=%d old_evidence=%d new_evidence=%d sections_diff=%s done_parts=%d next_parts=%d judge_pass=%t ms=%d",
		r.ID, r.Old.Narrative, r.New.Narrative, r.Old.Cards, r.New.Cards, r.Old.Evidence, r.New.Evidence, diff, r.DoneParts, r.NextParts, r.JudgePass, r.Ms)
}

// sectionsDiff names the composed sections whose item counts differ.
func sectionsDiff(old, composed map[string]int) []string {
	var out []string
	for _, name := range ComposedSectionNames() {
		if old[name] != composed[name] {
			out = append(out, name)
		}
	}
	return out
}

// writeShadowFile writes the copy atomically: a temp file in the shadow folder,
// then a rename over <id>.json. Nothing outside the shadow folder is touched.
func writeShadowFile(dir, id string, file shadowFile) (string, error) {
	shadowDir := filepath.Join(dir, ShadowDirName)
	if err := os.MkdirAll(shadowDir, 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(shadowDir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", err
	}
	final := filepath.Join(shadowDir, id+".json")
	if err := os.Rename(tmpName, final); err != nil {
		return "", err
	}
	return final, nil
}

// ReadShadowParts reads the done and next paragraph counts and whether the judge
// passed, from a stored shadow copy. It never returns the words.
func ReadShadowParts(dir, id string) (done, next int, pass bool, found bool) {
	if !idPattern.MatchString(id) {
		return 0, 0, false, false
	}
	data, err := os.ReadFile(filepath.Join(dir, ShadowDirName, id+".json"))
	if err != nil {
		return 0, 0, false, false
	}
	var file struct {
		Judge string `json:"judge"`
		Brief struct {
			NarrativeParts []NarrativePart `json:"narrativeParts"`
		} `json:"brief"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return 0, 0, false, false
	}
	done, next = partCounts(file.Brief.NarrativeParts)
	return done, next, file.Judge == JudgePass, true
}

// ReadShadowCounts reads the counts and section counts of a stored shadow copy.
// It returns only those two parts, so a caller that compares counts never sees
// the words.
func ReadShadowCounts(dir, id string) (ShadowCounts, map[string]int, bool) {
	if !idPattern.MatchString(id) {
		return ShadowCounts{}, nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, ShadowDirName, id+".json"))
	if err != nil {
		return ShadowCounts{}, nil, false
	}
	var file struct {
		Counts   ShadowCounts   `json:"counts"`
		Sections map[string]int `json:"sections"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return ShadowCounts{}, nil, false
	}
	return file.Counts, file.Sections, true
}
