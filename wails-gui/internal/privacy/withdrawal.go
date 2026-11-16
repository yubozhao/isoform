// Package privacy is the one place for the privacy state every saved copy and
// every reader checks: withdrawal events, the Closed Eyes pause and the stamps
// saved copies are served under. It must not import package main and has no
// network code: privacy data stays on the computer.
//
// See docs/privacy-module-2026-11-15.md.
package privacy

import "sync"

// ScopeKind names what a withdrawal covers.
type ScopeKind string

const (
	App          ScopeKind = "app"
	Window       ScopeKind = "window"
	Session      ScopeKind = "session"
	Source       ScopeKind = "source"
	TimeRange    ScopeKind = "time_range"
	Account      ScopeKind = "account"
	PolicyChange ScopeKind = "policy changed"
)

// Scope is what a withdrawal removed: an app, a window, a session, one named
// source, a time range, an account, or a policy change.
type Scope struct {
	Kind       ScopeKind
	SourceKind string
	ID         string
	StartMs    int64
	EndMs      int64
}

// Withdrawal is one event sent to every subscriber.
type Withdrawal struct {
	Scope Scope
	// Retention is true when retention cleanup removed the source: readers
	// drop raw copies, but it is not the person withdrawing anything.
	Retention bool
}

// Publisher sends each withdrawal to every subscriber synchronously, so a
// withdrawn source is gone from every reader before the action returns.
type Publisher struct {
	mu sync.RWMutex
	// first runs before every other subscriber (see SetFirst).
	first       func(Withdrawal)
	nextID      uint64
	subscribers map[uint64]func(Withdrawal)
}

// NewPublisher returns a publisher with no subscribers.
func NewPublisher() *Publisher {
	return &Publisher{subscribers: map[uint64]func(Withdrawal){}}
}

// Subscribe adds a subscriber and returns the function that removes it.
func (p *Publisher) Subscribe(subscriber func(Withdrawal)) func() {
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

// SetFirst sets the subscriber that runs before every other one, for the
// state the others read (the brief's privacy epoch). It is set once at start.
func (p *Publisher) SetFirst(subscriber func(Withdrawal)) {
	p.mu.Lock()
	p.first = subscriber
	p.mu.Unlock()
}

// Publish calls every subscriber with the withdrawal, outside the lock, so a
// subscriber may subscribe or unsubscribe. The first subscriber runs first.
func (p *Publisher) Publish(withdrawal Withdrawal) {
	p.mu.RLock()
	first := p.first
	subscribers := make([]func(Withdrawal), 0, len(p.subscribers))
	for _, subscriber := range p.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	p.mu.RUnlock()
	if first != nil {
		first(withdrawal)
	}
	for _, subscriber := range subscribers {
		subscriber(withdrawal)
	}
}
