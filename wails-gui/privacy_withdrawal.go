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
	// A withdrawn source is named, so the briefs that cite it are amended from
	// that alone. Other scopes (apps, sessions, accounts, time ranges) are not
	// named here and are amended with the gates.
	var keys []briefWithdrawn
	if scope.Kind == privacyWithdrawalSource && scope.ID != "" {
		keys = []briefWithdrawn{{Kind: scope.SourceKind, ID: scope.ID}}
	}
	briefPrivacyChanged(keys...)
	briefPrivacyAmendSoon()
	privacyWithdrawals.Publish(privacyWithdrawal{Scope: scope})
}
