package main

import "wails-gui/internal/privacy"

// The withdrawal types live in internal/privacy; these names keep the root
// package's callers unchanged.
type (
	privacyWithdrawalScopeKind = privacy.ScopeKind
	privacyWithdrawalScope     = privacy.Scope
	privacyWithdrawal          = privacy.Withdrawal
	privacyWithdrawalPublisher = privacy.Publisher
)

const (
	privacyWithdrawalApp          = privacy.App
	privacyWithdrawalWindow       = privacy.Window
	privacyWithdrawalSession      = privacy.Session
	privacyWithdrawalSource       = privacy.Source
	privacyWithdrawalTimeRange    = privacy.TimeRange
	privacyWithdrawalAccount      = privacy.Account
	privacyWithdrawalPolicyChange = privacy.PolicyChange
)

var privacyWithdrawals = privacy.NewPublisher()

// The brief runs before every other withdrawal subscriber, so its privacy
// epoch has moved before any of them reads it. Set in init, not in the var,
// because the brief's code reaches subscribers that refer to privacyWithdrawals.
func init() {
	privacyWithdrawals.SetFirst(briefOnPrivacyWithdrawal)
}

func subscribePrivacyWithdrawals(subscriber func(privacyWithdrawal)) func() {
	return privacyWithdrawals.Subscribe(subscriber)
}

// publishRetentionRemoval tells the readers of raw sources that retention
// removed one, without moving the brief privacy print: retention cleanup never
// withdraws or amends a sealed brief (docs/design/brief-seal-v2.md rule 2).
func publishRetentionRemoval(scope privacyWithdrawalScope) {
	privacyWithdrawals.Publish(privacyWithdrawal{Scope: scope, Retention: true})
}

func publishPrivacyWithdrawal(scope privacyWithdrawalScope) {
	privacyWithdrawals.Publish(privacyWithdrawal{Scope: scope})
}
