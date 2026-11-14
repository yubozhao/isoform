package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// While a rebuild holds the registry lock, a UI read may take the last-good
// result only when no privacy action happened since it was built. New activity
// alone does not block it (that used to fail the read with a deadline).
func TestTopicRegistryBusyReadNeedsUnchangedPrivacy(t *testing.T) {
	now := time.Now()
	built := topicRegistryRevision{privacy: 2, briefPrivacy: 5, forgets: 3, policy: [32]byte{1}}
	built.activitySources = topicActivityRevision{Generation: 1, Source: 10}
	state := topicsStateFile{}
	cache := &topicRegistryCache{revision: built, state: state, at: now.Add(-time.Second), until: now.Add(time.Minute)}

	newActivity := built
	newActivity.activitySources.Source = 11
	if !topicRegistryCachePrivacyMatches(cache, newActivity, state, now) {
		t.Fatal("new activity alone blocked the last-good result")
	}
	for name, change := range map[string]func(*topicRegistryRevision){
		"activity privacy epoch": func(r *topicRegistryRevision) { r.privacy += 2 },
		"app privacy epoch":      func(r *topicRegistryRevision) { r.briefPrivacy++ },
		"forget markers":         func(r *topicRegistryRevision) { r.forgets++ },
		"policy":                 func(r *topicRegistryRevision) { r.policy = [32]byte{2} },
		"closed eyes":            func(r *topicRegistryRevision) { r.closedEyes = true },
		"interests":              func(r *topicRegistryRevision) { r.interests.RefreshedAt = "changed" },
	} {
		revision := newActivity
		change(&revision)
		if topicRegistryCachePrivacyMatches(cache, revision, state, now) {
			t.Fatalf("a %s change still served the last-good result", name)
		}
	}
	if topicRegistryCachePrivacyMatches(cache, newActivity, state, now.Add(2*time.Minute)) {
		t.Fatal("an expired result was served")
	}
}

// A slow following step logs where its time went, with counts and durations
// only.
func TestFollowingTimingLogsBreakdownWithoutContent(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	timing := followingTiming{started: time.Now().Add(-2 * time.Second), interests: 4}
	timing.checked(1500 * time.Millisecond)
	timing.checked(10 * time.Millisecond)
	timing.log()
	out := buf.String()
	for _, want := range []string{"following_slow", "interests=4", "checks=2", "slowest_check_ms=1500"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log %q lacks %q", out, want)
		}
	}

	buf.Reset()
	(&followingTiming{started: time.Now()}).log()
	if buf.Len() != 0 {
		t.Fatalf("a fast following step logged: %q", buf.String())
	}
}
