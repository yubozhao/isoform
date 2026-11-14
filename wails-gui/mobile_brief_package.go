package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The brief package (PROTOCOL.md section 8.1): one immutable document per
// brief, stored as the exact bytes every route and push serves. Its version is
// the SHA-256 of those bytes, so the version a phone is told about always
// names the words it reads and the recording it plays.
const (
	mobileBriefPackageExt     = ".package"
	mobileBriefPackageMetaExt = ".package-meta"
	mobileBriefPackageSrcExt  = ".package-source"
	// mobileBriefPackagePartBytes is the slice of package bytes per part, the
	// same as Today's, so a part always fits one response envelope.
	mobileBriefPackagePartBytes = mobileTodayPartBytes
	// mobileBriefPackageInlineMax is the largest package GET /briefs/{id}
	// answers as one object; a larger one is served in parts.
	mobileBriefPackageInlineMax = 48 * 1024
	mobileBriefKindWeek         = "week"
)

var errMobileBriefPackageChanged = errors.New("version_changed")

// mobileBriefPackage is a stored package: its id, version and bytes.
type mobileBriefPackage struct {
	BriefID string
	Version string
	Bytes   []byte
}

// mobileBriefPackageMeta records how a package was made. GateKey is the read
// gates' result for the source snapshot when the package was written: a read
// that gets a different result writes a new package from the same snapshot.
type mobileBriefPackageMeta struct {
	Schema         int    `json:"schema"`
	BriefID        string `json:"briefId"`
	PackageVersion string `json:"packageVersion"`
	GateKey        string `json:"gateKey"`
	SourceAsOf     string `json:"sourceAsOf"`
	AudioKey       string `json:"audioKey"`
}

func mobileBriefPackageVersion(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// mobileBriefPackageParts slices the package bytes for chunked reads.
func mobileBriefPackageParts(data []byte) []string {
	parts := make([]string, 0, (len(data)+mobileBriefPackagePartBytes-1)/mobileBriefPackagePartBytes)
	for len(data) > 0 {
		n := min(len(data), mobileBriefPackagePartBytes)
		parts = append(parts, base64.RawURLEncoding.EncodeToString(data[:n]))
		data = data[n:]
	}
	return parts
}

// mobileBriefAudioKey names the recording a stored brief carries, so a new
// recording writes a new package. Status alone changes it too (downloading).
func mobileBriefAudioKey(brief MobileBriefV1) string {
	if brief.Audio == nil {
		return ""
	}
	key := brief.Audio.Status + "|" + brief.Audio.SHA256
	if brief.Audio.Attachment != nil {
		key += "|" + brief.Audio.Attachment.ID
	}
	return key
}

// savePackage writes the package bytes, the source snapshot it was made from
// and its metadata. The metadata is written last, so a package is read only
// once all three are complete.
func (s *mobileBriefStore) savePackage(pkg mobileBriefPackage, meta mobileBriefPackageMeta, source MobileBriefV1) error {
	if s.dir == "" {
		return errMobileBriefStateUnavailable
	}
	if !mobileBriefIDPattern.MatchString(pkg.BriefID) {
		return errors.New("brief ID has an unsupported shape")
	}
	snapshot, err := marshalStoredMobileBrief(source)
	if err != nil {
		return err
	}
	meta.Schema, meta.BriefID, meta.PackageVersion = 1, pkg.BriefID, pkg.Version
	encodedMeta, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if err := writeMobileBriefFileAtomic(s.dir, pkg.BriefID+mobileBriefPackageExt, pkg.Bytes); err != nil {
		return err
	}
	if err := writeMobileBriefFileAtomic(s.dir, pkg.BriefID+mobileBriefPackageSrcExt, snapshot); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(s.dir, pkg.BriefID+mobileBriefPackageMetaExt, encodedMeta)
}

// loadPackage returns a stored package with its metadata and source
// snapshot. A package whose bytes no longer match its recorded version is
// treated as missing.
func (s *mobileBriefStore) loadPackage(briefID string) (mobileBriefPackage, mobileBriefPackageMeta, MobileBriefV1, bool) {
	if s.dir == "" || !mobileBriefIDPattern.MatchString(briefID) {
		return mobileBriefPackage{}, mobileBriefPackageMeta{}, MobileBriefV1{}, false
	}
	rawMeta, err := os.ReadFile(filepath.Join(s.dir, briefID+mobileBriefPackageMetaExt))
	if err != nil {
		return mobileBriefPackage{}, mobileBriefPackageMeta{}, MobileBriefV1{}, false
	}
	var meta mobileBriefPackageMeta
	if json.Unmarshal(rawMeta, &meta) != nil || meta.BriefID != briefID {
		return mobileBriefPackage{}, mobileBriefPackageMeta{}, MobileBriefV1{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, briefID+mobileBriefPackageExt))
	if err != nil || mobileBriefPackageVersion(data) != meta.PackageVersion {
		return mobileBriefPackage{}, mobileBriefPackageMeta{}, MobileBriefV1{}, false
	}
	rawSource, err := os.ReadFile(filepath.Join(s.dir, briefID+mobileBriefPackageSrcExt))
	var source MobileBriefV1
	if err != nil || unmarshalStoredMobileBrief(rawSource, &source) != nil || source.BriefID != briefID {
		return mobileBriefPackage{}, mobileBriefPackageMeta{}, MobileBriefV1{}, false
	}
	return mobileBriefPackage{BriefID: briefID, Version: meta.PackageVersion, Bytes: data}, meta, source, true
}

// mobileBriefPackageKind names the card title: morning, evening, the week, or
// a brief someone asked for.
func mobileBriefPackageKind(brief MobileBriefV1) string {
	switch {
	case brief.YourWeek != nil:
		return mobileBriefKindWeek
	case brief.Slot == mobileBriefSlotMorning, brief.Slot == mobileBriefSlotEvening:
		return brief.Slot
	}
	return mobileBriefSlotOnRequest
}

// mobileBriefPackageDueAt is when a scheduled brief was due, from its own
// record of a late generation or else the schedule's clock on its date.
func mobileBriefPackageDueAt(brief MobileBriefV1, settings mobileBriefSettings) string {
	if brief.Generation != nil && brief.Generation.ScheduledAt != "" {
		return brief.Generation.ScheduledAt
	}
	clock := ""
	switch brief.Slot {
	case mobileBriefSlotMorning:
		clock = settings.Morning
	case mobileBriefSlotEvening:
		clock = settings.Evening
	default:
		return ""
	}
	if clock == "" {
		return ""
	}
	loc, err := time.LoadLocation(brief.Timezone)
	if err != nil {
		loc = time.Local
	}
	due, err := time.ParseInLocation("2006-01-02 15:04", brief.LocalDate+" "+clock, loc)
	if err != nil {
		return ""
	}
	return due.Format(time.RFC3339)
}

var (
	mobileBriefGreetingEN = regexp.MustCompile(`(?i)^(?:good\s+(?:morning|afternoon|evening|night)|morning|evening|hi|hello|hey)(?:\s+there)?(?:\s*[,，]\s*[\pL][\pL.'’-]*(?:\s+[\pL][\pL.'’-]*){0,2}|\s+[\pL][\pL'’-]*)?\s*[.!。！]?$`)
	mobileBriefGreetingZH = regexp.MustCompile(`^(?:早上好|早安|上午好|中午好|下午好|晚上好|晚安|你好|您好|嗨)(?:[,，、]?\s*[\pL]{0,12})?\s*[。！!.]?$`)
)

var (
	mobileBriefLeadGreetingEN = regexp.MustCompile(`^\s*(?i:good)\s+(?i:(morning|afternoon|evening))(?:\s*,\s*\p{Lu}[\pL'’-]*(?:\s+\p{Lu}[\pL'’-]*)?)?\s*[.!,]\s*`)
	mobileBriefLeadGreetingZH = regexp.MustCompile(`^\s*(早上好|早安|上午好|下午好|晚上好)(?:[,，、]\s*\p{Latin}[\p{Latin}'’-]*)?\s*[。！!，,.]\s*`)
)

// mobileBriefWithMatchingGreeting removes an opening greeting that does not
// match the time the brief was made: before noon morning, until 5 PM
// afternoon, then evening. The greeting matches the time, or there is none
// (today-review v1.1.16 D14).
func mobileBriefWithMatchingGreeting(brief MobileBriefV1) MobileBriefV1 {
	at := mobileBriefAsOfTime(brief)
	if len(brief.Narrative) == 0 || at.IsZero() {
		return brief
	}
	period := "evening"
	switch hour := at.In(mobileBriefNarrativeLocation(brief)).Hour(); {
	case hour < 12:
		period = "morning"
	case hour < 17:
		period = "afternoon"
	}
	text := brief.Narrative[0].Text
	greeting := ""
	match := mobileBriefLeadGreetingEN.FindStringSubmatch(text)
	if match != nil {
		greeting = strings.ToLower(match[1])
	} else if match = mobileBriefLeadGreetingZH.FindStringSubmatch(text); match != nil {
		switch match[1] {
		case "早上好", "早安", "上午好":
			greeting = "morning"
		case "下午好":
			greeting = "afternoon"
		default:
			greeting = "evening"
		}
	}
	if match == nil || greeting == period {
		return brief
	}
	rest := strings.TrimSpace(text[len(match[0]):])
	if rest == "" {
		return brief
	}
	if first, size := utf8.DecodeRuneInString(rest); unicode.IsLower(first) {
		rest = string(unicode.ToUpper(first)) + rest[size:]
	}
	narrative := append([]MobileBriefPara{}, brief.Narrative...)
	narrative[0].Text = rest
	brief.Narrative = narrative
	return brief
}

// mobileBriefIsGreeting reports a paragraph that only greets: one short
// sentence such as "Good morning." or "Good morning, Bo." or "早上好。".
func mobileBriefIsGreeting(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 48 {
		return false
	}
	return mobileBriefGreetingEN.MatchString(text) || mobileBriefGreetingZH.MatchString(text)
}

// mobileBriefWithJoinedGreeting joins a greeting that stands as the first
// paragraph to the first real sentence, so no reader ever shows a greeting on
// its own. Chips and spoken paragraph starts that point at later paragraphs
// move with them.
func mobileBriefWithJoinedGreeting(brief MobileBriefV1) MobileBriefV1 {
	if len(brief.Narrative) < 2 || !mobileBriefIsGreeting(brief.Narrative[0].Text) {
		return brief
	}
	greeting := strings.TrimSpace(brief.Narrative[0].Text)
	next := brief.Narrative[1]
	separator := " "
	if last, _ := utf8.DecodeLastRuneInString(greeting); last == '。' || last == '！' {
		separator = ""
	}
	next.Text = greeting + separator + strings.TrimSpace(next.Text)
	refs := append([]string{}, brief.Narrative[0].Refs...)
	for _, ref := range next.Refs {
		seen := false
		for _, have := range refs {
			seen = seen || have == ref
		}
		if !seen {
			refs = append(refs, ref)
		}
	}
	next.Refs = refs
	narrative := append([]MobileBriefPara{next}, brief.Narrative[2:]...)
	brief.Narrative = narrative
	if brief.Cards != nil {
		cards := make([]MobileBriefCard, 0, len(brief.Cards))
		for _, card := range brief.Cards {
			if card.Paragraph != nil {
				index := *card.Paragraph
				if index > 0 {
					index--
				}
				card.Paragraph = &index
			}
			cards = append(cards, card)
		}
		brief.Cards = cards
	}
	if brief.Audio != nil && brief.Audio.Paragraphs != nil {
		audio := *brief.Audio
		starts := make([]MobileBriefAudioParagraph, 0, len(audio.Paragraphs))
		for _, start := range audio.Paragraphs {
			if start.Source == mobileBriefAudioSourceNarrative {
				if start.Index == 0 {
					continue
				}
				start.Index--
				if start.Index == 0 {
					start.StartSeconds = 0
				}
			}
			starts = append(starts, start)
		}
		audio.Paragraphs = starts
		brief.Audio = &audio
	}
	return brief
}

// mobileBriefPackageDocument is the phone document a package holds: the
// phone projection after the read gates, without live data, with the
// greeting joined and the kind and due time named.
func (a *App) mobileBriefPackageDocument(source MobileBriefV1, settings mobileBriefSettings) MobileBriefV1 {
	var phone MobileBriefV1
	if a == nil {
		phone = source
	} else {
		phone, _ = a.mobileBriefPhoneProjection(source)
	}
	return mobileBriefPackageFinish(phone, settings)
}

// mobileBriefPackageFinish is the part of a package after the phone
// projection. It is idempotent, so a queued push rechecked against current
// policy gets the same bytes as a fresh package when nothing changed.
func mobileBriefPackageFinish(phone MobileBriefV1, settings mobileBriefSettings) MobileBriefV1 {
	phone = mobileBriefWithoutLiveDayShape(phone)
	phone = mobileBriefWithJoinedGreeting(phone)
	phone.Kind = mobileBriefPackageKind(phone)
	phone.DueAt = mobileBriefPackageDueAt(phone, settings)
	return phone
}

// briefSettings is the schedule packages name due times with.
func (bridge *mobileBridge) briefSettings() mobileBriefSettings {
	if source, ok := bridge.briefSource().(*mobileBriefService); ok && source != nil {
		return source.config.load()
	}
	return defaultMobileBriefSettings()
}

// mobileBriefPackageGateKey is the read gates' result for a source snapshot.
// It changes only when current policy withdraws something from the brief or
// the display language changes.
func (a *App) mobileBriefPackageGateKey(source MobileBriefV1) string {
	return a.mobileBriefPackageGateKeyContext(context.Background(), source)
}

func (a *App) mobileBriefPackageGateKeyContext(ctx context.Context, source MobileBriefV1) string {
	if a == nil {
		return ""
	}
	if key, ok := a.mobileBriefSealedGateKey(source); ok {
		return key
	}
	encoded, err := json.Marshal(a.mobileBriefForDeliveryContext(ctx, source))
	if err != nil {
		return ""
	}
	return mobileBriefPackageVersion(encoded)
}

// mobileBriefSealedRead is a stored brief as the computer reads it: its sealed
// copy (mobile_brief_finished_read.go), so the phone follows seal v2. Only an
// explicit privacy action changes the copy, and a belief correction shows its
// new wording instead of only dropping the old row. It reports false when
// source is not the stored brief (a draft, a test document), there is no brief
// service, or the read is held for a passing reason (a store still opening);
// the caller then projects source itself, as before.
func (a *App) mobileBriefSealedRead(source MobileBriefV1) (MobileBriefV1, bool) {
	service, stored, ok := a.mobileBriefSealedSource(source)
	if !ok {
		return MobileBriefV1{}, false
	}
	brief, withheld, err := service.finishedReadWithholding(stored)
	if err != nil || withheld.Transient() || brief.BriefID != source.BriefID {
		return MobileBriefV1{}, false
	}
	return brief, true
}

// mobileBriefSealedSource is the brief service and the stored brief source
// names, when the phone may read its sealed copy. Closed Eyes is a passing
// state the live projection already withholds for, so it keeps that path.
func (a *App) mobileBriefSealedSource(source MobileBriefV1) (*mobileBriefService, MobileBriefV1, bool) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return nil, MobileBriefV1{}, false
	}
	service, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || service == nil || service.store == nil {
		return nil, MobileBriefV1{}, false
	}
	stored, found := service.storedSource(source.BriefID)
	if !found || stored.AsOf != source.AsOf {
		return nil, MobileBriefV1{}, false
	}
	return service, stored, true
}

// mobileBriefSealedGateKey names the privacy state a package read from the
// sealed copy depends on: the print and day the copy is sealed under, and the
// display language, and the copy's words. It changes when an explicit privacy
// action amends the copy (seal v2), never when a Today refresh re-seals live
// rows, so those never change a package by themselves. The read first brings
// the copy up to date.
func (a *App) mobileBriefSealedGateKey(source MobileBriefV1) (string, bool) {
	service, stored, ok := a.mobileBriefSealedSource(source)
	if !ok {
		return "", false
	}
	read, withheld, err := service.finishedReadWithholding(stored)
	if err != nil || withheld.Transient() {
		return "", false
	}
	saved, found, err := service.store.loadFinishedRead(source.BriefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		return "", false
	}
	// The words and the parts a gate can withdraw also name the key, so a copy
	// sealed again with less in it reaches the phone. Live rows (the day's
	// shape, finds) stay out of it.
	words, err := json.Marshal(struct {
		Narrative         []MobileBriefPara
		WeeklyRecap       []MobileBriefPara
		YourWeek          *MobileBriefWeek
		MeetingAttendance []MobileBriefAttendance
		Audio             bool
	}{read.Narrative, read.WeeklyRecap, read.YourWeek, read.MeetingAttendance, read.Audio != nil})
	if err != nil {
		return "", false
	}
	return mobileBriefPackageVersion([]byte("sealed\x00" + saved.Print + "\x00" + saved.Day + "\x00" + a.modelDisplayLanguage() + "\x00" + string(words))), true
}

// buildMobileBriefPackage makes a package from a source brief without
// storing it.
func (a *App) buildMobileBriefPackage(source MobileBriefV1, settings mobileBriefSettings) (mobileBriefPackage, MobileBriefV1, error) {
	document := a.mobileBriefPackageDocument(source, settings)
	data, err := json.Marshal(document)
	if err != nil {
		return mobileBriefPackage{}, MobileBriefV1{}, err
	}
	return mobileBriefPackage{BriefID: source.BriefID, Version: mobileBriefPackageVersion(data), Bytes: data}, document, nil
}

// BriefPackage returns the stored package of a brief, writing it first when
// none exists, the stored brief was generated again or gained a recording,
// or the read gates now say something different about its snapshot. Live
// rows saved into the brief later (finds) never change a package by
// themselves. Quiet slots have no package.
func (s *mobileBriefService) BriefPackage(briefID string) (mobileBriefPackage, bool) {
	return s.BriefPackageContext(context.Background(), briefID)
}

// BriefPackageContext is BriefPackage with a context: a pass context carries
// one noticed lookup across every brief of the pass.
func (s *mobileBriefService) BriefPackageContext(ctx context.Context, briefID string) (mobileBriefPackage, bool) {
	if !s.beginWork() {
		return mobileBriefPackage{}, false
	}
	defer s.wg.Done()
	s.packageMu.Lock()
	defer s.packageMu.Unlock()
	stored, found := s.store.load(briefID)
	if !found || stored.NarrativeStatus == "quiet" {
		return mobileBriefPackage{}, false
	}
	pkg, meta, snapshot, ok := s.store.loadPackage(briefID)
	if ok && snapshot.AsOf == stored.AsOf && meta.AudioKey == mobileBriefAudioKey(stored) {
		if meta.GateKey == s.app.mobileBriefPackageGateKeyContext(ctx, snapshot) {
			return pkg, true
		}
		// A source the package was made from was withdrawn: the package is
		// replaced by its own source as it may be read now, never by live
		// rows. A passing reason (Closed Eyes) keeps the source for later.
		stored = snapshot
		if readable, withheld := mobileBriefTodayReadableSynthesis(s.app, snapshot); !withheld.Transient() {
			stored = readable
		}
	}
	pkg, err := s.writePackageLocked(stored)
	if err != nil {
		return mobileBriefPackage{}, false
	}
	return pkg, true
}

// writePackage stores a new package for a source brief. Generation and the
// audio step call it so a push carries the same bytes a read returns.
func (s *mobileBriefService) writePackage(source MobileBriefV1) (mobileBriefPackage, error) {
	s.packageMu.Lock()
	defer s.packageMu.Unlock()
	return s.writePackageLocked(source)
}

func (s *mobileBriefService) writePackageLocked(source MobileBriefV1) (mobileBriefPackage, error) {
	// The gates are read before and after the build: a policy change while
	// building would otherwise store old bytes under the new gate key, and
	// a later read would keep serving them.
	for attempt := 0; ; attempt++ {
		gate := s.app.mobileBriefPackageGateKey(source)
		pkg, document, err := s.app.buildMobileBriefPackage(source, s.config.load())
		if err != nil {
			return mobileBriefPackage{}, err
		}
		if document.NarrativeStatus == "quiet" {
			return mobileBriefPackage{}, errMobileBriefNoPackage
		}
		if gate != s.app.mobileBriefPackageGateKey(source) {
			if attempt < 2 {
				continue
			}
			// Policy keeps changing: serve this build, store nothing.
			return pkg, nil
		}
		meta := mobileBriefPackageMeta{GateKey: gate, SourceAsOf: source.AsOf, AudioKey: mobileBriefAudioKey(source)}
		if err := s.store.savePackage(pkg, meta, source); err != nil {
			return mobileBriefPackage{}, err
		}
		return pkg, nil
	}
}

var errMobileBriefNoPackage = errors.New("this brief has no package")

// mobileBriefPackager is the optional part of a brief source that stores
// packages. A source without it (unit-test fakes) has its packages built on
// each read from the delivered brief.
type mobileBriefPackager interface {
	BriefPackage(briefID string) (mobileBriefPackage, bool)
}

// briefPackage returns the package every phone route serves for a brief.
func (bridge *mobileBridge) briefPackage(briefID string) (mobileBriefPackage, bool) {
	return bridge.briefPackageContext(context.Background(), briefID)
}

// mobileBriefPackagerContext is the package read that takes a pass context.
type mobileBriefPackagerContext interface {
	BriefPackageContext(ctx context.Context, briefID string) (mobileBriefPackage, bool)
}

func (bridge *mobileBridge) briefPackageContext(ctx context.Context, briefID string) (mobileBriefPackage, bool) {
	svc := bridge.briefSource()
	if svc == nil {
		return mobileBriefPackage{}, false
	}
	if packager, ok := svc.(mobileBriefPackagerContext); ok {
		if pkg, ok := packager.BriefPackageContext(ctx, briefID); ok {
			return pkg, true
		}
	} else if packager, ok := svc.(mobileBriefPackager); ok {
		if pkg, ok := packager.BriefPackage(briefID); ok {
			return pkg, true
		}
	}
	brief, ok := svc.GetMobileBrief(briefID)
	if !ok || brief.NarrativeStatus == "quiet" {
		return mobileBriefPackage{}, false
	}
	pkg, _, err := bridge.app.buildMobileBriefPackage(brief, bridge.briefSettings())
	return pkg, err == nil
}
