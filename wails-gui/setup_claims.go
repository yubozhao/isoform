package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// setup_claims.go builds setup step 2, "Is this right?" (computer setup
// v1.0.0 rule 5, redesign-shell v1.0.0 rule 3): three to five claims about
// the person from the calendar and mail, read just now, each with where it
// came from. One model call, through the person's own AI. Each claim is saved
// as an Inferred belief in About you, so Yes, No and Edit answer it the way
// You does (ConfirmMemoryEntry, NotQuiteMemoryEntry) and the answers land in
// Your answers. Only metadata is read: who met whom, meeting titles, mail
// senders and subjects; never a mail body.

const (
	setupClaimsCap        = 5
	setupClaimsSource     = "setup-claims"
	setupClaimsModelWait  = 90 * time.Second
	setupClaimsMailDays   = 14
	setupClaimsMailPeople = 8
	setupClaimsSubjects   = 3
	setupClaimsPeople     = 12
)

// SetupClaim is one claim step 2 shows. Source is "calendar" or "mail"; At is
// the day of the evidence it came from (unix ms).
type SetupClaim struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Source string `json:"source"`
	At     int64  `json:"at"`
}

// SetupClaims is step 2's read. Status is "ready", or "nothing" when there
// was nothing to read (no permission, Closed Eyes, no events or mail) or the
// model found nothing it could say.
type SetupClaims struct {
	Status string       `json:"status"`
	Claims []SetupClaim `json:"claims"`
}

// setupClaimsModelCall is the one model entry point. Tests replace it.
var setupClaimsModelCall = func(ctx context.Context, a *App, prompt string) (string, error) {
	return a.runEphemeralClaudeModelContext(ctx, prompt, a.lightTaskModel(), setupClaimsModelWait)
}

// setupClaimsOnce keeps step 2 to one build per run: going back to the step
// shows the same claims instead of asking the model again.
var setupClaimsOnce struct {
	mu   sync.Mutex
	done bool
	last SetupClaims
}

// setupClaimEvidence is one numbered line the model may cite.
type setupClaimEvidence struct {
	Source string
	At     int64
	Line   string
}

// BuildSetupClaims reads the calendar and mail and returns step 2's claims.
func (a *App) BuildSetupClaims() (SetupClaims, error) {
	setupClaimsOnce.mu.Lock()
	defer setupClaimsOnce.mu.Unlock()
	if setupClaimsOnce.done {
		return a.setupClaimsStillShown(setupClaimsOnce.last), nil
	}
	out, err := a.buildSetupClaims(context.Background(), time.Now())
	if err != nil {
		return SetupClaims{Status: "nothing", Claims: []SetupClaim{}}, err
	}
	setupClaimsOnce.done, setupClaimsOnce.last = true, out
	return out, nil
}

// setupClaimsStillShown drops a claim that was forgotten since it was built.
func (a *App) setupClaimsStillShown(last SetupClaims) SetupClaims {
	entries, err := a.GetMemoryEntries()
	if err != nil {
		return last
	}
	have := map[string]bool{}
	for _, entry := range entries {
		have[entry.ID] = true
	}
	out := SetupClaims{Status: last.Status, Claims: []SetupClaim{}}
	for _, claim := range last.Claims {
		if have[claim.ID] {
			out.Claims = append(out.Claims, claim)
		}
	}
	if len(out.Claims) == 0 {
		out.Status = "nothing"
	}
	return out
}

func (a *App) buildSetupClaims(ctx context.Context, now time.Time) (SetupClaims, error) {
	nothing := SetupClaims{Status: "nothing", Claims: []SetupClaim{}}
	if a == nil || a.store == nil {
		return nothing, errors.New("the store is not ready")
	}
	if a.IsClosedEyesModeEnabled() {
		return nothing, nil
	}
	evidence := a.setupClaimsCalendarEvidence(now)
	evidence = append(evidence, a.setupClaimsMailEvidence(ctx, now)...)
	if len(evidence) == 0 {
		return nothing, nil
	}
	prompt := buildSetupClaimsPrompt(evidence, uiLanguageDisplayName(a.currentUILanguage()))
	raw, err := setupClaimsModelCall(ctx, a, prompt)
	if err != nil {
		log.Printf("[setup-claims] model call failed: %v", err)
		return nothing, nil
	}
	claims := parseSetupClaims(raw, evidence)
	saved := a.storeSetupClaims(claims, now)
	if len(saved) == 0 {
		return nothing, nil
	}
	log.Printf("[setup-claims] built claims=%d evidence=%d", len(saved), len(evidence))
	return SetupClaims{Status: "ready", Claims: saved}, nil
}

// setupClaimsCalendarEvidence is this week's people from the calendar, behind
// the calendar's own privacy gate (calendarFirstDayAt).
func (a *App) setupClaimsCalendarEvidence(now time.Time) []setupClaimEvidence {
	day, err := a.calendarFirstDayAt(now)
	if err != nil || day.Paused || day.ReadsOff {
		return nil
	}
	var out []setupClaimEvidence
	for i, person := range day.People {
		if i == setupClaimsPeople {
			break
		}
		name := strings.TrimSpace(person.Name)
		if name == "" {
			continue
		}
		line := fmt.Sprintf("Calendar: %d meeting(s) this week with %s", person.MeetingsThisWeek, name)
		if title := strings.TrimSpace(person.NextMeetingTitle); title != "" {
			line += fmt.Sprintf("; next: \"%s\"", title)
		}
		if person.Recurring {
			line += " (recurring)"
		}
		at := person.NextMeetingAt
		if at == 0 {
			at = now.UnixMilli()
		}
		out = append(out, setupClaimEvidence{Source: "calendar", At: at, Line: line})
	}
	return out
}

// setupClaimsMailEvidence is who writes to the person most, with a few
// subjects each, from the mail already read (senders and subjects only).
func (a *App) setupClaimsMailEvidence(ctx context.Context, now time.Time) []setupClaimEvidence {
	if !a.captureAllowed(ctx, "setup_claims", mailPermissionKey) || (a.mailSync != nil && !a.mailSync.readingAllowed()) {
		return nil
	}
	rows, err := a.store.ListMailMessages(ctx, now.AddDate(0, 0, -setupClaimsMailDays).UnixMilli())
	if err != nil {
		return nil
	}
	return setupClaimsMailLines(rows)
}

func setupClaimsMailLines(rows []storage.MailMessageRow) []setupClaimEvidence {
	type sender struct {
		name     string
		count    int
		last     int64
		subjects []string
	}
	by := map[string]*sender{}
	for _, row := range rows {
		if row.Mailbox != storage.MailboxInbox || !row.ToMe {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(row.SenderEmail))
		name := strings.TrimSpace(row.SenderName)
		if key == "" || name == "" {
			continue
		}
		s := by[key]
		if s == nil {
			s = &sender{name: name}
			by[key] = s
		}
		s.count++
		if row.At > s.last {
			s.last = row.At
		}
		subject := strings.TrimSpace(row.Subject)
		if subject != "" && len(s.subjects) < setupClaimsSubjects && !setupClaimsHas(s.subjects, subject) {
			s.subjects = append(s.subjects, subject)
		}
	}
	senders := make([]*sender, 0, len(by))
	for _, s := range by {
		senders = append(senders, s)
	}
	sort.Slice(senders, func(i, j int) bool {
		if senders[i].count != senders[j].count {
			return senders[i].count > senders[j].count
		}
		return senders[i].last > senders[j].last
	})
	var out []setupClaimEvidence
	for i, s := range senders {
		if i == setupClaimsMailPeople {
			break
		}
		line := fmt.Sprintf("Mail: %d message(s) from %s in two weeks", s.count, s.name)
		if len(s.subjects) > 0 {
			line += "; subjects: \"" + strings.Join(s.subjects, "\", \"") + "\""
		}
		out = append(out, setupClaimEvidence{Source: "mail", At: s.last, Line: line})
	}
	return out
}

func setupClaimsHas(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func buildSetupClaimsPrompt(evidence []setupClaimEvidence, languageName string) string {
	var b strings.Builder
	b.WriteString("You read who a person met and who wrote to them this week, from their calendar and mail, ")
	b.WriteString("and say a few things about their work that they can confirm or correct. Use only the lines below.\n\n")
	b.WriteString("Write every text value in " + languageName + ".\n\n")
	b.WriteString("Rules:\n")
	b.WriteString(fmt.Sprintf("- claims: 3 to %d short sentences in the second person, each about the person's work: who they work with, on what, and how often. ", setupClaimsCap))
	b.WriteString("For example \"You meet Lena most weeks, about the Halcyon import.\" or \"Priya runs Thursday's review.\"\n")
	b.WriteString("- Each claim names the one line number (L1, L2...) it rests on most, as evidence.\n")
	b.WriteString("- Never guess beyond the lines. Never include contact details, health, family, money or anything private about anyone.\n")
	b.WriteString("- Never include secrets, keys, tokens, passwords or account identifiers.\n")
	b.WriteString("- Fewer claims are better than a weak one. Leave the list empty rather than guess.\n\n")
	b.WriteString("Reply with one JSON object only, in this shape:\n")
	b.WriteString(`{"claims":[{"text":"","evidence":1}]}`)
	b.WriteString("\n\nLines:\n")
	for i, item := range evidence {
		b.WriteString(fmt.Sprintf("L%d. %s\n", i+1, item.Line))
	}
	return b.String()
}

type setupClaimsReply struct {
	Claims []struct {
		Text     string `json:"text"`
		Evidence int    `json:"evidence"`
	} `json:"claims"`
}

// parseSetupClaims checks the reply: each claim is trimmed, capped, dropped
// when it looks secret or names something the privacy filter would hide, and
// takes its source and day from the line it cites, never from the model.
func parseSetupClaims(raw string, evidence []setupClaimEvidence) []SetupClaim {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil
	}
	var reply setupClaimsReply
	if json.Unmarshal([]byte(raw[start:end+1]), &reply) != nil {
		return nil
	}
	var out []SetupClaim
	seen := map[string]bool{}
	for _, item := range reply.Claims {
		if len(out) == setupClaimsCap {
			break
		}
		text := checkedAssistantHistoryText(item.Text, 200)
		if text == "" || pii.HasRedaction(text) || memoryTextIsPromptOrTemplate(text) {
			continue
		}
		if item.Evidence < 1 || item.Evidence > len(evidence) {
			continue
		}
		key := strings.ToLower(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		line := evidence[item.Evidence-1]
		out = append(out, SetupClaim{Text: beliefInSecondPerson(text), Source: line.Source, At: line.At})
	}
	return out
}

// storeSetupClaims saves each claim as an Inferred belief with its evidence,
// so You shows it under New this week and Why can say where it came from.
// A claim already in About you in nearly the same words is not shown again.
func (a *App) storeSetupClaims(claims []SetupClaim, now time.Time) []SetupClaim {
	if len(claims) == 0 {
		return nil
	}
	entries, err := a.GetMemoryEntries()
	if err != nil {
		return nil
	}
	stored := make([]assistantHistoryStoredBelief, 0, len(entries))
	for _, entry := range entries {
		stored = append(stored, assistantHistoryStoredBeliefOf(entry))
	}
	var out []SetupClaim
	for _, claim := range claims {
		if _, ok := matchAssistantHistoryBelief(stored, claim.Text); ok {
			continue
		}
		claim.ID = assistantHistoryRecordID("memory-setup-", claim.Source, claim.Text)
		fields := map[string]any{
			"type":         "Isoform Memory",
			"title":        claim.Text,
			"timestamp":    okfTimestamp(now),
			"source":       setupClaimsSource,
			"dimension":    normalizeMemoryDimension(""),
			"area":         classifyMemoryArea(claim.Text, ""),
			"setup_source": claim.Source,
			"setup_seen":   okfTimestamp(time.UnixMilli(claim.At)),
		}
		content := renderOKFDocument(fields, "## Memory Points\n\n- "+claim.Text)
		if _, err := a.saveMemoryEntry(claim.ID, content, false); err != nil {
			if !errors.Is(err, errMemoryForgotten) {
				log.Printf("[setup-claims] claim not saved: %v", err)
			}
			continue
		}
		stored = append(stored, assistantHistoryStoredBelief{id: claim.ID, title: claim.Text, shape: newBeliefShape(claim.Text)})
		out = append(out, claim)
	}
	return out
}

// ReviseSetupClaim saves the person's own words for a claim (E in step 2).
// It keeps the claim's evidence; the words are marked as the person's.
func (a *App) ReviseSetupClaim(id, text string) error {
	text = strings.Join(strings.Fields(text), " ")
	if !strings.HasPrefix(id, "memory-setup-") || text == "" {
		return errors.New("that claim can't be changed here")
	}
	setupClaimsOnce.mu.Lock()
	var claim SetupClaim
	found := false
	for _, c := range setupClaimsOnce.last.Claims {
		if c.ID == id {
			claim, found = c, true
		}
	}
	setupClaimsOnce.mu.Unlock()
	if !found {
		return errors.New("that claim is gone")
	}
	fields := map[string]any{
		"type":         "Isoform Memory",
		"title":        text,
		"timestamp":    okfTimestamp(time.Now()),
		"source":       setupClaimsSource,
		"dimension":    normalizeMemoryDimension(""),
		"area":         classifyMemoryArea(text, ""),
		"setup_source": claim.Source,
		"setup_seen":   okfTimestamp(time.UnixMilli(claim.At)),
	}
	if _, err := a.saveMemoryEntry(id, renderOKFDocument(fields, "## Memory Points\n\n- "+text), true); err != nil {
		return err
	}
	setupClaimsOnce.mu.Lock()
	for i := range setupClaimsOnce.last.Claims {
		if setupClaimsOnce.last.Claims[i].ID == id {
			setupClaimsOnce.last.Claims[i].Text = text
		}
	}
	setupClaimsOnce.mu.Unlock()
	return nil
}
