package main

import "sync"

type privacyWithdrawalScopeKind string

const (
	privacyWithdrawalApp          privacyWithdrawalScopeKind = "app"
	privacyWithdrawalWindow       privacyWithdrawalScopeKind = "window"
	privacyWithdrawalSession      privacyWithdrawalScopeKind = "session"
	privacyWithdrawalSource       privacyWithdrawalScopeKind = "source"
	privacyWithdrawalTimeRange    privacyWithdrawalScopeKind = "time_range"
	privacyWithdrawalAccount      privacyWithdrawalScopeKind = "account"
	privacyWithdrawalPolicyChange privacyWithdrawalScopeKind = "policy changed"
)

type privacyWithdrawalScope struct {
	Kind       privacyWithdrawalScopeKind
	SourceKind string
	ID         string
	StartMs    int64
	EndMs      int64
}

type privacyWithdrawal struct {
	Scope privacyWithdrawalScope
}

type privacyWithdrawalPublisher struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]func(privacyWithdrawal)
}

func newPrivacyWithdrawalPublisher() *privacyWithdrawalPublisher {
	return &privacyWithdrawalPublisher{subscribers: map[uint64]func(privacyWithdrawal){}}
}

func (p *privacyWithdrawalPublisher) subscribe(subscriber func(privacyWithdrawal)) func() {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.subscribers[id] = subscriber
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.subscribers, id)
		p.mu.Unlock()
	}
}

func (p *privacyWithdrawalPublisher) publish(withdrawal privacyWithdrawal) {
	p.mu.RLock()
	subscribers := make([]func(privacyWithdrawal), 0, len(p.subscribers))
	for _, subscriber := range p.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	p.mu.RUnlock()
	for _, subscriber := range subscribers {
		subscriber(withdrawal)
	}
}

var privacyWithdrawals = newPrivacyWithdrawalPublisher()

func subscribePrivacyWithdrawals(subscriber func(privacyWithdrawal)) func() {
	return privacyWithdrawals.subscribe(subscriber)
}

// publishRetentionRemoval tells the readers of raw sources that retention
// removed one, without moving the brief privacy print: retention cleanup never
// withdraws or amends a sealed brief (docs/design/brief-seal-v2.md rule 2).
func publishRetentionRemoval(scope privacyWithdrawalScope) {
	privacyWithdrawals.publish(privacyWithdrawal{Scope: scope})
}

func publishPrivacyWithdrawal(scope privacyWithdrawalScope) {
	// A withdrawn source is named, so the briefs that cite it are amended from
	// that alone. Other scopes (apps, sessions, accounts, time ranges) are not
	// named here and are amended with the gates.
	var keys []briefWithdrawn
	if scope.Kind == privacyWithdrawalSource && scope.ID != "" {
		keys = []briefWithdrawn{{Kind: scope.SourceKind, ID: scope.ID}}
	}
	briefPrivacyChanged(keys...)
	briefPrivacyAmendSoon()
	privacyWithdrawals.publish(privacyWithdrawal{Scope: scope})
}
