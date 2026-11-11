// Package brief holds the daily brief pipeline: gather, compose, seal, deliver
// and amend. This first step has the types and a read-only Reader over the
// files the app already writes under <data>/mobile/briefs. Nothing in the app
// uses this package yet, and the package must not import package main.
//
// The wire shape is the one defined by mobile-protocol/PROTOCOL.md section 8.
// The field names and JSON tags below are copied from MobileBriefV1 and are
// checked against it by a test in package main, so a rename there fails there.
package brief

import (
	"context"
	"encoding/json"
	"time"
)

// Window is the span of source activity a brief covers, in the brief's own
// timezone. Slot is morning, evening or on_request.
type Window struct {
	Slot      string
	LocalDate string
	Timezone  string
	Start     time.Time
	End       time.Time
}

// SourceRef names one source a brief cites: the kind and id as the brief's
// evidence refs spell them (the same keys a privacy withdrawal names).
type SourceRef struct {
	Kind  string
	ID    string
	Label string
}

// Snapshot is what the gather stage read from the sources for one Window at one
// time. It is input to composing a brief, never shown as it is.
type Snapshot struct {
	Window  Window
	TakenAt time.Time
	Sources []SourceRef
}

// Inputs is a Snapshot prepared for composing: the language and privacy state
// the compose and seal stages need, with the snapshot's sources.
type Inputs struct {
	Snapshot Snapshot
	Language string
	// Print is the privacy print the inputs were gathered under (the app's
	// briefPrivacyPrint). A sealed brief keeps the print it was checked under.
	Print string
}

// Withholding says why words were left out of a sealed copy. Reason uses the
// app's names (source_withdrawn, store_unavailable, privacy_check, ...). The
// fields mirror withheld_reason and withheld_kind in the copy file.
type Withholding struct {
	Reason     string
	SourceKind string
}

// Withdrawn names one source a privacy action withdrew. An empty Kind matches
// any kind with that ID. Its JSON matches the withdrawal log entries.
type Withdrawn struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id"`
}

// PrivacyChange is one privacy epoch change. Withdrawn is empty when the change
// named no sources; the amend then runs the gates again for each brief.
type PrivacyChange struct {
	Epoch     uint64      `json:"epoch"`
	Withdrawn []Withdrawn `json:"keys,omitempty"`
}

// RequestResult answers an on-demand generation request. Its JSON matches
// MobileBriefRequestResult.
type RequestResult struct {
	Status        string `json:"status"` // started | running | cooldown | queued | making
	NextAllowedAt string `json:"nextAllowedAt"`
	ReadyAt       string `json:"readyAt,omitempty"`
}

// Counts are the parts of a brief a reader can count without showing them.
type Counts struct {
	Narrative int
	Cards     int
	Evidence  int
	Audio     bool
}

// Paragraph is one written paragraph (MobileBriefPara). Only the fields the
// reader needs are typed; the rest of the paragraph stays in the stored JSON.
type Paragraph struct {
	Text string   `json:"text"`
	Refs []string `json:"refs"`
}

// Evidence is one source row the brief cites (MobileBriefRef).
type Evidence struct {
	Ref        string `json:"ref"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	Label      string `json:"label"`
	ObservedAt string `json:"observedAt"`
}

// Generation explains a brief made ahead of its slot (MobileBriefGeneration).
type Generation struct {
	Reason      string `json:"reason"`
	ScheduledAt string `json:"scheduledAt"`
	GeneratedAt string `json:"generatedAt"`
	Message     string `json:"message"`
}

// Document is the brief a phone reads (MobileBriefV1). Sections, day shape,
// attendance, offers, weekly parts and the other typed parts stay in the raw
// JSON: they depend on types still in package main, and the reader does not
// need them. Cards and Audio are kept raw for the same reason; only their
// counts are used. Reading a Document never changes the stored bytes.
type Document struct {
	Version         int               `json:"version"`
	BriefID         string            `json:"briefId"`
	Slot            string            `json:"slot"`
	DisplaySlot     string            `json:"displaySlot,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	DueAt           string            `json:"dueAt,omitempty"`
	LocalDate       string            `json:"localDate"`
	Timezone        string            `json:"timezone"`
	WindowStart     string            `json:"windowStart"`
	AsOf            string            `json:"asOf"`
	HubName         string            `json:"hubName"`
	ColdStart       bool              `json:"coldStart"`
	FromHistory     bool              `json:"fromHistory"`
	NarrativeStatus string            `json:"narrativeStatus"`
	ConversationKey string            `json:"conversationKey,omitempty"`
	Narrative       []Paragraph       `json:"narrative"`
	Evidence        []Evidence        `json:"evidence"`
	Cards           []json.RawMessage `json:"cards,omitempty"`
	WeeklyRecap     []Paragraph       `json:"weeklyRecap,omitempty"`
	Audio           json.RawMessage   `json:"audio,omitempty"`
	Generation      *Generation       `json:"generation,omitempty"`
}

// Sealed is the sealed copy of one brief as a reader sees it: the Document
// header and the bytes the phone reads, with the decision that produced them.
type Sealed struct {
	Document Document
	// Raw is the copy's brief JSON, returned to the phone unchanged.
	Raw      json.RawMessage
	Counts   Counts
	Decision Decision
	Withheld Withholding
}

// Service is the narrow interface the app uses. It is declared here so later
// stages can implement it; nothing implements it yet.
type Service interface {
	Start(ctx context.Context)
	Stop()
	Latest() (Sealed, bool)
	Get(id string) (Sealed, bool)
	Request(reason string) (RequestResult, error)
	Amend(ctx context.Context, change PrivacyChange) error
}

// Deps is what the service needs from the app. Declared for later stages; the
// app provides it when the service is wired in.
type Deps interface {
	Snapshot(ctx context.Context, w Window) (Inputs, error)
	Ask(ctx context.Context, prompt string) (string, error)
	Speak(ctx context.Context, text, lang string) ([]byte, error)
	ClosedEyes() bool
	Language() string
	Emit(name string, data any)
}
