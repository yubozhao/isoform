// Package handoff is the file-first store for Isoform one-time work: one JSON
// file per handoff under ~/.yansu-agent/handoff/<id>.json. It deliberately keeps
// no database: handoffs are few and queried by status, so a directory scan
// suffices (mirroring how the memory store lists entries). The package is pure
// (stdlib only) so it compiles and tests in isolation, without the cgo-heavy
// `package main`.
package handoff

// SchemaVersion is written into every handoff file. Version 3 uses JSON; older
// markdown records remain readable and migrate when explicitly changed.
const SchemaVersion = 3

// Status is the lifecycle state of a handoff. Waiting (`active`), `blocked` and
// `needs_review` handoffs surface in the composer chip and on Today; `stale` ones have aged out or been superseded and linger
// (greyed) on the Handoffs page until garbage-collected. `needs_review` is a
// finished run whose evidence did not verify: it waits for the user and never
// expires on its own.
type Status string

const (
	StatusActive      Status = "active"
	StatusInProgress  Status = "in_progress"
	StatusBlocked     Status = "blocked"
	StatusNeedsReview Status = "needs_review"
	StatusCompleted   Status = "completed"
	StatusStale       Status = "stale"
	StatusAborted     Status = "aborted"
)

// IsChipStatus reports whether a handoff status is one the user can act on now:
// waiting to start, blocked, or finished and needing review. These are the
// statuses that surface as composer chips and on Today.
func IsChipStatus(st Status) bool {
	switch st {
	case StatusActive, StatusBlocked, StatusNeedsReview:
		return true
	}
	return false
}

// Reasons an in_progress Handoff became blocked. The store keeps the code;
// the frontend owns the wording.
const (
	BlockedInterrupted = "interrupted" // no live run when the app started
	BlockedTimeout     = "timeout"     // no store activity for the lifecycle timeout
)

// Who stopped a task that did not finish.
const (
	StoppedByPerson = "person"
	StoppedBySystem = "system"
)

// Why a task stopped without finishing. The store keeps the code; the app
// owns the wording.
const (
	CauseNoAssistant    = "no_assistant"    // no AI is configured on this computer
	CauseSignedOut      = "signed_out"      // the CLI's own auth status says signed out
	CauseRestarted      = "restarted"       // Isoform restarted during the run
	CauseTimeout        = "timeout"         // no activity for the lifecycle timeout
	CauseAssistantError = "assistant_error" // anything else the assistant ended with
	CauseLoopGuard      = "loop_guard"      // the typed loop guard stopped the run
)

// Verification records how a finished run measured against its success
// criteria. Met counts the criteria whose evidence checked out; Note says what
// was missing when the run needs review.
type Verification struct {
	Met    int    `json:"met"`
	Total  int    `json:"total"`
	Note   string `json:"note,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Result is the final turn's structured claim. Only independent verification
// may promote the entry to completed; this block is never authority or proof.
type Result struct {
	Status   Status            `json:"status"`
	Criteria []ResultCriterion `json:"criteria"`
	Evidence []ResultEvidence  `json:"evidence"`
}

type ResultCriterion struct {
	Text     string `json:"text"`
	Met      bool   `json:"met"`
	Evidence []int  `json:"evidence"` // zero-based indexes into Result.Evidence
}

type ResultEvidence struct {
	Path       string `json:"path,omitempty"`
	URL        string `json:"url,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
}

// ListItem keeps the fields needed to continue a task explicitly added from a
// list. Project is an unresolved label and never an unchecked filesystem path.
type ListItem struct {
	Who         string `json:"who"`
	Person      string `json:"person,omitempty"`
	When        string `json:"when,omitempty"`
	Project     string `json:"project,omitempty"`
	ReminderID  string `json:"reminderId,omitempty"`
	DraftTaskID string `json:"draftTaskId,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	BatchID     string `json:"batchId,omitempty"`
	Original    string `json:"original,omitempty"`
}

type Source string

const (
	SourceTriage     Source = "triage"
	SourceFallback   Source = "suggestion-fallback"
	SourceOnboarding Source = "onboarding"
	SourceAgency     Source = "agency"
	// Inbox message-extractor sources: one per IM platform so handoff
	// provenance and dedup can distinguish where a task came from.
	SourceSlack    Source = "slack"
	SourceDiscord  Source = "discord"
	SourceTelegram Source = "telegram"
	// SourceMobile marks a handoff the user started from the paired iPhone.
	SourceMobile  Source = "mobile"
	SourceDesktop Source = "desktop"
)

// Entry is one handoff persisted as JSON, with its brief retained as markdown
// in Body. SchemaVersion is additive for old records; newer schemas are readable
// but cannot be overwritten by this version of the app.
type Entry struct {
	SchemaVersion    int      `json:"schemaVersion"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Status           Status   `json:"status"`
	Source           Source   `json:"source"`
	SourceID         string   `json:"sourceId"`
	SourceLink       string   `json:"sourceLink"` // permalink back to the originating message (inbox sources); empty for activity
	AppName          string   `json:"appName"`
	Workdir          string   `json:"workdir"`
	KeyFiles         []string `json:"keyFiles"`
	ChatSessionID    string   `json:"chatSessionId"`
	CronJobID        string   `json:"cronJobId"`                  // at-cron job created on accept (for cancel/cleanup)
	AttemptID        string   `json:"attemptId"`                  // one execution attempt; fences late run results
	AttemptStartedAt int64    `json:"attemptStartedAt,omitempty"` // unix ms; start of the current attempt
	LastActivityAt   int64    `json:"lastActivityAt"`             // unix ms; actual turn activity, not display changes
	BlockedReason    string   `json:"blockedReason"`              // set while Status is blocked: BlockedInterrupted or BlockedTimeout
	StoppedBy        string   `json:"stoppedBy,omitempty"`        // aborted: StoppedByPerson or StoppedBySystem
	StopCause        string   `json:"stopCause,omitempty"`        // aborted by the system: a Cause code
	StopAssistant    string   `json:"stopAssistant,omitempty"`    // runner the cause names: claude_code, codex or empty
	StoppedAt        int64    `json:"stoppedAt,omitempty"`        // unix ms the task stopped
	NoAIAtStart      bool     `json:"noAiAtStart,omitempty"`      // the start-time check found no AI configured or signed in
	Criteria         []string `json:"criteria"`                   // success criteria from the brief's acceptance section
	// Verification is set when a run ends: nil until then, then the check result.
	Verification *Verification `json:"verification,omitempty"`
	Result       *Result       `json:"result,omitempty"`
	CreatedAt    int64         `json:"createdAt"` // unix ms
	UpdatedAt    int64         `json:"updatedAt"` // unix ms
	Body         string        `json:"body"`      // the brief's markdown document
	// Thread is the task's conversation and outcome; nil on older records.
	Thread   *Thread   `json:"thread,omitempty"`
	ListItem *ListItem `json:"listItem,omitempty"`
	// Backend pins the task to the coding agent the person connected and
	// handed it to ("claude-code" or "codex"); empty runs on the app's
	// provider. It never falls back to another agent.
	Backend string `json:"backend,omitempty"`
}
