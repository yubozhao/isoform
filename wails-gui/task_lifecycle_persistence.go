package main

type TaskLifecyclePersistenceResult struct {
	ObservationKey string   `json:"observationKey"`
	ReasoningKind  string   `json:"reasoningKind,omitempty"`
	EvidenceIDs    []string `json:"evidenceIds"`
	TransitionIDs  []string `json:"transitionIds"`
	LinkIDs        []string `json:"linkIds"`
}
