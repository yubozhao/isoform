package main

// MobileBriefV1 is the document a paired iPhone reads. It is the wire shape
// defined in mobile-protocol/PROTOCOL.md section 8; the phone decodes every
// field as optional and ignores unknown fields, so additive changes stay in
// version 1. Arrays are never nil when marshalled.
type MobileBriefV1 struct {
	modelBinding    *mobileBriefModelBinding
	rankedEdition   bool
	rankedEmpty     bool
	ownVoiceFacts   bool
	generationDebug *mobileBriefGenerationDebug

	DisplaySlot string `json:"displaySlot,omitempty"`
	// Kind names the card title (morning, evening, week, on_request) and
	// DueAt is when a scheduled brief was due. Only packages carry them
	// (PROTOCOL.md section 8.1).
	Kind  string `json:"kind,omitempty"`
	DueAt string `json:"dueAt,omitempty"`

	Offers []MobileBriefOffer `json:"offers,omitempty"`
	// Cards are the brief's prepared clicks (mobile_brief_cards.go).
	Cards           []MobileBriefCard `json:"cards,omitempty"`
	ConversationKey string            `json:"conversationKey,omitempty"`
	Version         int               `json:"version"`
	BriefID         string            `json:"briefId"`
	Slot            string            `json:"slot"` // morning | evening | on_request
	LocalDate       string            `json:"localDate"`
	Timezone        string            `json:"timezone"`
	WindowStart     string            `json:"windowStart"`
	AsOf            string            `json:"asOf"`
	HubName         string            `json:"hubName"`
	ColdStart       bool              `json:"coldStart"`
	// FromHistory is true when the brief drew on this computer's Claude Code or
	// Codex history (AssistantHistoryView), so the screen can name it.
	FromHistory     bool              `json:"fromHistory"`
	NarrativeStatus string            `json:"narrativeStatus"` // ok | unavailable | skipped
	Narrative       []MobileBriefPara `json:"narrative"`
	// NarrativeParts splits Narrative into its done and next parts, in order
	// (mobile_brief_forward.go). Additive: phones read Narrative alone.
	NarrativeParts []MobileBriefNarrativePart `json:"narrativeParts,omitempty"`
	NeedsYouCount  int                        `json:"needsYouCount"` // always 0 since the Desk was removed; kept so older phones decode
	Sections       MobileBriefSections        `json:"sections"`
	DayShape       *MobileBriefDay            `json:"dayShape"`
	Counts         MobileBriefCounts          `json:"counts"`
	Evidence       []MobileBriefRef           `json:"evidence"`
	Limits         MobileBriefLimits          `json:"limits"`
	// WeeklyRecap is "Your week" as paragraphs, for readers that show
	// paragraphs and for the recording; YourWeek is the same week as a card
	// (your_week.go). Both exist only on the Friday evening brief.
	WeeklyRecap   []MobileBriefPara         `json:"weeklyRecap,omitempty"`
	YourWeek      *MobileBriefWeek          `json:"yourWeek,omitempty"`
	WeekQuestion  *MorningBriefWeekQuestion `json:"weekQuestion,omitempty"`
	WeekLead      string                    `json:"-"`
	WeekLeadFacts string                    `json:"-"`
	// Audio describes the spoken brief or its voice download status
	// (docs/notifications-design.md 7).
	Audio *MobileBriefAudio `json:"audio,omitempty"`
	// Additional local signals retained with the brief, also read by Today.
	Upcoming []MobileBriefItem `json:"upcoming,omitempty"`
	// Generation explains a scheduled brief recovered after observed sleep.
	Generation *MobileBriefGeneration `json:"generation,omitempty"`
	// MeetingAttendance is an observation on this computer, not a claim
	// about meetings on another device. Older phones ignore this field.
	MeetingAttendance []MobileBriefAttendance `json:"meetingAttendance,omitempty"`
}

type MobileBriefAttendance struct {
	// Local provenance is serialized only by mobileBriefStore, never by the
	// wire encoder. Nil means a legacy row without checked dependencies.
	sourceSessionIDs []string

	EventID string   `json:"eventId"`
	Title   string   `json:"title"`
	State   string   `json:"state"` // attended | skipped | unknown
	Reason  string   `json:"reason"`
	Scope   string   `json:"scope"` // computer
	Refs    []string `json:"refs"`
}

type MobileBriefGeneration struct {
	Reason      string `json:"reason"`
	ScheduledAt string `json:"scheduledAt"`
	GeneratedAt string `json:"generatedAt"`
	Message     string `json:"message"`
}

type MobileBriefPara struct {
	Hold      *TodayDayShapeHold    `json:"hold,omitempty"`
	TopicID   string                `json:"topicId,omitempty"`
	TopicName string                `json:"topicName,omitempty"`
	Role      yourWeekParagraphRole `json:"-"`
	// Part is done or next while the writer's paragraphs are being checked;
	// the wire form is the narrativeParts count list instead.
	Part      string `json:"-"`
	chipWords *mobileBriefCardWords
	Text      string   `json:"text"`
	Refs      []string `json:"refs"`
}

type MobileBriefSections struct {
	Noticed       []MobileBriefItem `json:"noticed,omitempty"`
	Decide        []MobileBriefItem `json:"decide,omitempty"`
	Review        []MobileBriefItem `json:"review,omitempty"`
	Plan          []MobileBriefItem `json:"plan,omitempty"`
	WaitingOnYou  []MobileBriefItem `json:"waitingOnYou,omitempty"`
	Commitments   []MobileBriefItem `json:"commitments,omitempty"`
	WorkingToward []MobileBriefItem `json:"workingToward,omitempty"`
	DayAhead      []MobileBriefItem `json:"dayAhead,omitempty"`
	Highlights    []MobileBriefItem `json:"highlights,omitempty"`
	Moved         []MobileBriefItem `json:"moved"`
	Doing         []MobileBriefItem `json:"doing"`
	Watching      []MobileBriefItem `json:"watching"`
	Meetings      []MobileBriefItem `json:"meetings"`
	People        []MobileBriefItem `json:"people"`
	OpenLoops     []MobileBriefItem `json:"openLoops"`
	Learned       []MobileBriefItem `json:"learned"`
	Deliverables  []MobileBriefItem `json:"deliverables"`
	Schedules     []MobileBriefItem `json:"schedules"`
}

type MobileBriefItem struct {
	Outcome *ActivityThreadOutcome `json:"outcome,omitempty"`
	ID      string                 `json:"id"`
	// Key names the item by its section and source record, so the same record
	// keeps its key in every brief that shows it (PROTOCOL.md section 20.2).
	// Omitted from briefs stored before keys; older phones ignore it.
	Key    string `json:"key,omitempty"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status string `json:"status"`
	At     string `json:"at"`
	// EndAt lets readers expire meetings without trusting a cached status.
	EndAt string   `json:"endAt,omitempty"`
	Kind  string   `json:"kind"`
	Refs  []string `json:"refs"`
	// URL is a web link to open the item: a meeting's join link or a find.
	// http and https only; omitted otherwise, and older phones ignore it.
	URL           string `json:"url,omitempty"`
	LastTouch     string `json:"lastTouch,omitempty"`
	NextStep      string `json:"nextStep,omitempty"`
	DueAt         string `json:"dueAt,omitempty"`
	MomentID      string `json:"momentId,omitempty"`
	ChatSessionID string `json:"chatSessionId,omitempty"`
	ChatMessageID string `json:"chatMessageId,omitempty"`
	// Since is when Isoform first recorded the item (RFC 3339). Readers name a
	// carried-over item's age in the brief's timezone. Omitted when unknown.
	Since string `json:"since,omitempty"`
	// AttentionReason is one short line in the brief's language saying why the
	// item needs the user, from its grounding only; empty when it does not.
	// AttentionRank orders those items, 1 first (docs/today-page.md).
	AttentionReason string `json:"attentionReason,omitempty"`
	AttentionRank   int    `json:"attentionRank,omitempty"`
	// Missing historical activity permits title/state only, never inferred progress.
	EvidenceUnavailable bool                    `json:"evidenceUnavailable,omitempty"`
	PurposeID           string                  `json:"purposeId,omitempty"`
	Context             *MobileBriefItemContext `json:"context,omitempty"`
}

type MobileBriefDay struct {
	*TodayDayShape
	ActiveMinutes int                  `json:"activeMinutes"`
	Hours         []MobileBriefHour    `json:"hours"`
	TopApps       []MobileBriefAppTime `json:"topApps"`
	// From the context signals: the first and last input of the day and
	// the minutes spent away (lock, sleep or a long idle). Omitted when the
	// Mac has not seen the day start yet; older phones ignore them.
	FirstActive  string `json:"firstActive,omitempty"`
	LastActive   string `json:"lastActive,omitempty"`
	BreakMinutes int    `json:"breakMinutes,omitempty"`
}

type MobileBriefHour struct {
	Hour          int `json:"hour"`
	ActiveMinutes int `json:"activeMinutes"`
}

type MobileBriefAppTime struct {
	Name          string `json:"name"`
	ActiveMinutes int    `json:"activeMinutes"`
}

type MobileBriefCounts struct {
	OpenLoopsByKind    map[string]int `json:"openLoopsByKind"`
	SelfUpdatesPending int            `json:"selfUpdatesPending"` // always 0 since the Desk was removed
	LearningItems      int            `json:"learningItems"`
	NeedsYou           int            `json:"needsYou"` // always 0 since the Desk was removed
}

type MobileBriefRef struct {
	Ref        string `json:"ref"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	Label      string `json:"label"`
	ObservedAt string `json:"observedAt"`
	// WebSource keeps capture identity after browser history is pruned. It is
	// provenance for the read gate, never permission to share the derived text.
	WebSource *MobileBriefWebSource `json:"webSource,omitempty"`
}

type MobileBriefWebSource struct {
	Browsers []string `json:"browsers"`
	StartMs  int64    `json:"startMs"`
	EndMs    int64    `json:"endMs"`
}

type MobileBriefLimits struct {
	ItemsPerSection        int      `json:"itemsPerSection"`
	EvidenceRefs           int      `json:"evidenceRefs"`
	TruncatedSections      []string `json:"truncatedSections"`
	ExcludedSensitiveItems int      `json:"excludedSensitiveItems,omitempty"`
}

// MobileBriefSummary is the list row for GET /briefs and the History tab.
type MobileBriefSummary struct {
	BriefID         string `json:"briefId"`
	Slot            string `json:"slot"`
	LocalDate       string `json:"localDate"`
	AsOf            string `json:"asOf"`
	NarrativeStatus string `json:"narrativeStatus"`
	Lead            string `json:"lead,omitempty"`
}

// MobileBriefSchedule is the Wails-facing schedule view (PROTOCOL.md section 12).
type MobileBriefSchedule struct {
	Enabled            bool   `json:"enabled"`
	Morning            string `json:"morning"`
	Evening            string `json:"evening"`
	LastGeneratedAt    string `json:"lastGeneratedAt"`
	LastSlot           string `json:"lastSlot"`
	NarrativeAvailable bool   `json:"narrativeAvailable"`
	// FeedEnabled is the private podcast feed switch; FeedURL is where the
	// feed is served on this Mac while the switch is on.
	FeedEnabled bool   `json:"feedEnabled"`
	FeedURL     string `json:"feedUrl"`
	// A pointer preserves the saved choice when an older client omits it.
	AdaptiveTiming    *bool  `json:"adaptiveTiming,omitempty"`
	NextPredictedAt   string `json:"nextPredictedAt,omitempty"`
	NextPredictedSlot string `json:"nextPredictedSlot,omitempty"`
	TimingReason      string `json:"timingReason,omitempty"`
}

// MobileBriefAudioData is the stored spoken brief handed to the Mac's Today
// page: the file as base64 with its type and length.
type MobileBriefAudioData struct {
	BriefID         string  `json:"briefId"`
	Status          string  `json:"status,omitempty"`
	StatusMessage   string  `json:"statusMessage,omitempty"`
	Mime            string  `json:"mime"`
	Base64          string  `json:"base64"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// MobileBriefRequestResult answers an on-demand generation request.
type MobileBriefRequestResult struct {
	Status        string `json:"status"` // started | running | cooldown | queued | making
	NextAllowedAt string `json:"nextAllowedAt"`
	ReadyAt       string `json:"readyAt,omitempty"`
}

// MobileBriefItemContext preserves the preview and action target for a source
// already represented in a section. Older readers ignore these additions.
type MobileBriefItemContext struct {
	SourceLabel   string                   `json:"sourceLabel,omitempty"`
	Preview       string                   `json:"preview,omitempty"`
	Schedule      string                   `json:"schedule,omitempty"` // Legacy stored documents only.
	ScheduleSpec  *MobileBriefScheduleSpec `json:"scheduleSpec,omitempty"`
	JobID         string                   `json:"jobId,omitempty"`
	FileCount     int                      `json:"fileCount,omitempty"`
	BlockedReason string                   `json:"blockedReason,omitempty"`
}

// MobileBriefScheduleSpec carries machine values; desktop readers translate
// them with the same schedule formatter as Automations.
type MobileBriefScheduleSpec struct {
	Kind       string `json:"kind"`
	Expression string `json:"expression,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	EveryMS    int64  `json:"everyMs,omitempty"`
	AtMS       int64  `json:"atMs,omitempty"`
	NextRunAt  int64  `json:"nextRunAt"`
}
