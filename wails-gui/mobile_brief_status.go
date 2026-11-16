package main

import (
	"encoding/json"
	"time"
)

// mobileBriefStatus is what the phone reads about the brief (PROTOCOL.md 8.2):
// the one current brief, a build in progress, and the next scheduled one. It
// is served in GET /status, GET /today and the pairing approval, so the phone
// can say "being made", "comes when your computer wakes" or "your first brief
// comes at" from facts, never from guesses.
type mobileBriefStatus struct {
	Current *mobileBriefStatusCurrent `json:"current,omitempty"`
	Making  *mobileBriefStatusMaking  `json:"making,omitempty"`
	Next    *mobileBriefStatusNext    `json:"next,omitempty"`
}

type mobileBriefStatusCurrent struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	MadeAt  string `json:"madeAt"`
	Audio   bool   `json:"audio"`
}

type mobileBriefStatusMaking struct {
	Kind      string `json:"kind"`
	Slot      string `json:"slot"`
	StartedAt string `json:"startedAt"`
	ReadyAt   string `json:"readyAt"`
}

type mobileBriefStatusNext struct {
	Kind  string `json:"kind"`
	Slot  string `json:"slot"`
	DueAt string `json:"dueAt"`
}

// mobileBriefMakingEstimate is how long a build is said to take when the
// phone asks; the phone request already promises the same 90 seconds.
const mobileBriefMakingEstimate = 90 * time.Second

// briefStatus reads the brief's status for the phone. It never builds or
// writes anything; nil means the brief service isn't available.
func (bridge *mobileBridge) briefStatus(now time.Time) *mobileBriefStatus {
	svc := bridge.briefSource()
	if svc == nil {
		return nil
	}
	status := &mobileBriefStatus{}
	if current, ok := currentMobileBrief(svc); ok {
		if pkg, ok := bridge.briefPackage(current.BriefID); ok {
			status.Current = &mobileBriefStatusCurrent{
				ID: current.BriefID, Version: pkg.Version, Kind: mobileBriefPackageKind(current),
				MadeAt: current.AsOf, Audio: mobileBriefPackageHasAudio(pkg.Bytes),
			}
		}
	}
	if schedule, err := svc.Schedule(); err == nil {
		if slots := mobileBriefSlotTimes(now, time.Local, schedule); len(slots) > 0 {
			status.Next = &mobileBriefStatusNext{Kind: slots[0].Slot, Slot: slots[0].Slot, DueAt: slots[0].At}
		}
	}
	if source, ok := svc.(*mobileBriefService); ok {
		status.Making = source.makingStatus()
	}
	return status
}

// cachedBriefStatus is the status Today's snapshot holds, so GET /status,
// which phones ask often, reads no package. Before the first snapshot it is
// read directly.
func (bridge *mobileBridge) cachedBriefStatus() *mobileBriefStatus {
	bridge.today.mu.RLock()
	value := bridge.today.value
	bridge.today.mu.RUnlock()
	if value != nil && value.BriefStatus != nil {
		status := *value.BriefStatus
		return &status
	}
	return bridge.briefStatus(time.Now())
}

// makingStatus says which brief is being built, or nil. A brief made ahead of
// its slot has no running slot: the phone gets it only at the slot's time.
func (s *mobileBriefService) makingStatus() *mobileBriefStatusMaking {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.runningSlot == "" || s.runningSince.IsZero() {
		return nil
	}
	readyAt := s.runningSince.Add(mobileBriefMakingEstimate)
	if s.phoneRequest != nil && s.phoneRequest.state == "making" && !s.phoneRequest.readyAt.IsZero() {
		readyAt = s.phoneRequest.readyAt
	}
	return &mobileBriefStatusMaking{
		Kind: s.runningSlot, Slot: s.runningSlot,
		StartedAt: formatMobileBriefTime(s.runningSince), ReadyAt: formatMobileBriefTime(readyAt),
	}
}

// mobileBriefPackageHasAudio says whether a package carries a recording the
// phone can download.
func mobileBriefPackageHasAudio(data []byte) bool {
	var head struct {
		Audio *struct {
			Attachment json.RawMessage `json:"attachment"`
		} `json:"audio"`
	}
	if json.Unmarshal(data, &head) != nil || head.Audio == nil {
		return false
	}
	return len(head.Audio.Attachment) > 0 && string(head.Audio.Attachment) != "null"
}
