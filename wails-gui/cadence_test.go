package main

import (
	"context"
	"testing"
	"time"
)

// TestCadenceTable pins each background loop to the cadence Bo approved on
// 2026-11-12, so a change to one is a deliberate edit of cadence.go.
func TestCadenceTable(t *testing.T) {
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"activity batch (triage, memory, summaries, composer, semantic, threads)", activityBatchInterval, time.Hour},
		{"thread outcomes", cadenceThreadOutcomes, 24 * time.Hour},
		{"self-reflection", selfDreamSchedulerMinInterval, 24 * time.Hour},
		{"purpose understanding", purposeUnderstandingPeriodicInterval, 24 * time.Hour},
		{"engagement purpose check", cadenceEngagementPurpose, 24 * time.Hour},
		{"engagement capture health", engagementCaptureInterval, time.Hour},
		{"background learning", cadenceBackgroundLearning, 24 * time.Hour},
		{"suggestions", suggestionMinInterval, 24 * time.Hour},
		{"activity embedding", cadenceActivityEmbedding, time.Hour},
		{"assistant history", assistantHistoryLoopEvery, time.Hour},
		{"slack and feishu scan", incrementalPollInterval, time.Hour},
		{"telegram scan", telegramPollInterval, time.Hour},
		{"discord scan", discordIncrementalPollInterval, time.Hour},
		{"slack memory", memoryCheckInterval, time.Hour},
		{"reminders", remindersSyncInterval, time.Hour},
		{"contacts", cadenceContacts, 24 * time.Hour},
		{"phone Today", cadencePhoneToday, time.Hour},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s runs every %s, want %s", c.name, c.got, c.want)
		}
	}
	// A daily job checks its clock hourly; it must never check less often
	// than the hourly work, or a day could be skipped.
	if cadenceCheck > time.Hour {
		t.Fatalf("daily jobs check every %s", cadenceCheck)
	}
}

func TestThreadOutcomesRunOnceADayUnlessRunNow(t *testing.T) {
	triager, _, _ := newBatchTestTriager(t)
	now := time.Now()
	triager.runDerivedActivityLayers(context.Background(), false)
	if triager.app.derivedRunDue("thread_outcomes", cadenceThreadOutcomes, now.Add(time.Hour)) {
		t.Fatal("the batch did not record today's outcome pass")
	}
	// A later hourly batch the same day leaves outcomes alone.
	triager.runDerivedActivityLayers(context.Background(), false)
	if triager.app.derivedRunDue("thread_outcomes", cadenceThreadOutcomes, now.Add(2*time.Hour)) {
		t.Fatal("the outcome clock moved on a skipped pass")
	}
	if !triager.app.derivedRunDue("thread_outcomes", cadenceThreadOutcomes, now.Add(25*time.Hour)) {
		t.Fatal("outcomes are not due the next day")
	}
}

func TestBriefPrerunRunsTheBatchOnceForAScheduledSlot(t *testing.T) {
	triager, store, runner := newBatchTestTriager(t)
	triager.app.activity.triager = triager
	hour := time.Now().Add(-time.Hour).Truncate(time.Minute)
	saveBatchTestSession(t, store, 1, hour, [2]string{"Notes", "Sheets"}, [2]string{"Quarter plan", "Budget"})

	// A brief the person asked for does not wait for the batch.
	triager.app.prerunActivityForBrief(context.Background(), mobileBriefSlotOnRequest, time.Now())
	triager.wg.Wait()
	if got := runner.count(); got != 0 {
		t.Fatalf("an on-request brief ran the batch: %d calls", got)
	}

	triager.app.prerunActivityForBrief(context.Background(), mobileBriefSlotMorning, time.Now())
	triager.wg.Wait()
	if got := runner.count(); got != 2 {
		t.Fatalf("scheduled brief pre-run made %d calls, want 2 (triage and memory)", got)
	}
	pending, err := store.GetPendingSessions()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pre-run left %d sessions pending (%v)", len(pending), err)
	}

	// Once per slot: a retry of the same morning brief does not run it again.
	saveBatchTestSession(t, store, 2, hour.Add(30*time.Minute), [2]string{"Mail", "Docs"}, [2]string{"Reply", "Draft"})
	triager.app.prerunActivityForBrief(context.Background(), mobileBriefSlotMorning, time.Now())
	triager.wg.Wait()
	if got := runner.count(); got != 2 {
		t.Fatalf("second pre-run for the same slot made calls: %d", got)
	}
}

func TestRunNowIsImmediateRightAfterABatch(t *testing.T) {
	triager, store, runner := newBatchTestTriager(t)
	if err := triager.runBatch(context.Background(), "hourly"); err != nil {
		t.Fatal(err)
	}
	saveBatchTestSession(t, store, 1, time.Now().Add(-10*time.Minute), [2]string{"Notes", "Sheets"}, [2]string{"Quarter plan", "Budget"})
	// "Run now" right after the hourly batch still runs, and runs the daily
	// outcome layer too.
	if err := triager.runBatch(context.Background(), "manual"); err != nil {
		t.Fatal(err)
	}
	triager.wg.Wait()
	if got := runner.count(); got != 2 {
		t.Fatalf("Run now made %d calls, want 2", got)
	}
}
