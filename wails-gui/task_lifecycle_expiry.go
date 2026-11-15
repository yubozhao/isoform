package main

type TaskLifecycleObservationLease struct {
	PolicyVersion      string `json:"policyVersion"`
	Status             string `json:"status"`
	Open               bool   `json:"open"`
	AnchorKind         string `json:"anchorKind"`
	AnchorTransitionID string `json:"anchorTransitionId,omitempty"`
	AnchorAt           int64  `json:"anchorAt"`
	HorizonMs          int64  `json:"horizonMs"`
	DeadlineAt         int64  `json:"deadlineAt"`
	Due                bool   `json:"due"`
	EvidenceRenews     bool   `json:"evidenceRenews"`
}

type TaskLifecycleExpiryResult struct {
	PolicyVersion   string   `json:"policyVersion"`
	ScannedCount    int      `json:"scannedCount"`
	ExpiredIDs      []string `json:"expiredIds"`
	Errors          []string `json:"errors"`
	BacklogDeferred bool     `json:"backlogDeferred"`
}
