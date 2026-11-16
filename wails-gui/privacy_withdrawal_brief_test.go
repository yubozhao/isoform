package main

import "testing"

// The brief subscribes to withdrawals first, so every other subscriber already
// sees the advanced privacy epoch; a retention removal never moves it.
func TestBriefPrivacyEpochMovesBeforeOtherWithdrawalSubscribers(t *testing.T) {
	before := mobileBriefPrivacyEpoch.Load()
	var seen uint64
	unsubscribe := subscribePrivacyWithdrawals(func(privacyWithdrawal) {
		seen = mobileBriefPrivacyEpoch.Load()
	})
	t.Cleanup(unsubscribe)

	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "m-order"})
	if seen <= before {
		t.Fatalf("subscriber saw epoch %d, want it past %d", seen, before)
	}
	keys, ok := withdrawnSince(seen - 1)
	if !ok || len(keys) != 1 || keys[0].ID != "m-order" || keys[0].Kind != "memory" {
		t.Fatalf("withdrawn since = %+v %v, want the named source", keys, ok)
	}

	after := mobileBriefPrivacyEpoch.Load()
	publishRetentionRemoval(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "file", ID: "file:old"})
	if got := mobileBriefPrivacyEpoch.Load(); got != after {
		t.Fatalf("retention moved the epoch from %d to %d", after, got)
	}
}
