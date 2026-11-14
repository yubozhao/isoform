package main

// morningBriefCandidate contains only facts admitted by the source readers.
// Unknown attributes are left empty or false; a missing observation is not a
// negative fact, an obligation, or authority to act.
type morningBriefCandidate struct {
	SourceKind       string                         `json:"-"`
	SourceID         string                         `json:"-"`
	SourceDigest     string                         `json:"-"`
	SourceOrigin     string                         `json:"-"`
	BriefRole        string                         `json:"briefRole,omitempty"`
	Refs             []string                       `json:"-"`
	Dependencies     []morningBriefSourceDependency `json:"-"`
	Unbound          bool                           `json:"-"`
	ID               string                         `json:"id"`
	Title            string                         `json:"title"`
	Status           string                         `json:"status,omitempty"`
	LastTouch        string                         `json:"lastTouch,omitempty"`
	NextStep         string                         `json:"nextStep,omitempty"`
	Due              string                         `json:"due,omitempty"`
	HardDueToday     bool                           `json:"hardDueToday"`
	HardDueKnown     bool                           `json:"hardDueKnown"`
	DaysOpen         int                            `json:"daysOpen"`
	DaysOpenKnown    bool                           `json:"daysOpenKnown"`
	Counterparty     string                         `json:"counterparty,omitempty"`
	Attendees        []string                       `json:"attendees,omitempty"`
	Direction        string                         `json:"direction,omitempty"`
	DaysWaiting      int                            `json:"daysWaiting"`
	DaysWaitingKnown bool                           `json:"daysWaitingKnown"`
	Serves           string                         `json:"serves,omitempty"`
	ServesKnown      bool                           `json:"servesKnown"`
	OnlyUser         bool                           `json:"onlyUser"`
	OnlyUserKnown    bool                           `json:"onlyUserKnown"`
	Changed          bool                           `json:"changedSinceYesterday"`
	ChangeKnown      bool                           `json:"changeKnown"`
	Change           string                         `json:"change,omitempty"`
	PriorBriefs      []string                       `json:"priorBriefs,omitempty"`
	Confidence       float64                        `json:"confidence"`
	MeetingPrep      bool                           `json:"meetingPrep"`
	Within24Hours    bool                           `json:"within24Hours"`
	Slipping         bool                           `json:"slipping"`
	Handled          bool                           `json:"handled"`
	NeedsUser        bool                           `json:"needsUser"`
	DecisionKind     string                         `json:"decisionKind,omitempty"`
	DependencyCount  int                            `json:"dependencyCount"`
}
