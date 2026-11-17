package main

import (
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

// The forget-before cutoff (internal/privacy): Forget everything stamps it
// when it starts, and every derived build that began before it saves nothing.
var (
	forgetCutoffOnce sync.Once
	forgetCutoffVal  *privacy.Cutoff
)

func init() { storage.AutoPrincipalsQuiet = derivedSavesQuiet }

func forgetCutoff() *privacy.Cutoff {
	forgetCutoffOnce.Do(func() {
		dir, _ := flavorDataDir()
		forgetCutoffVal = privacy.NewCutoff(dir)
	})
	return forgetCutoffVal
}

// derivedBuildStart is the token a derived build takes when it begins.
func derivedBuildStart() time.Time { return forgetCutoff().Start() }

// derivedBuildMaySave reports whether a build that began at start may save:
// false once Forget everything started after it.
func derivedBuildMaySave(start time.Time) bool { return forgetCutoff().Allows(start) }

// forgetQuietAfter is how long after Forget everything finishes the builds
// with no privacy token of their own still save nothing: long enough for a
// build that began before the job to end.
const forgetQuietAfter = 15 * time.Minute

// forgetQuietUntil is, in unix ms, when derived saves resume: the max while the
// job runs or waits, the end of the window once it is done, 0 when none ran.
var forgetQuietUntil atomic.Int64

var errForgetQuiet = errors.New("Isoform is forgetting everything; nothing learned before it is saved")

// noteForgetQuiet follows the job's state into the quiet window.
func noteForgetQuiet(status ForgetEverythingStatus) {
	switch status.State {
	case forgetRunning, forgetPending:
		forgetQuietUntil.Store(math.MaxInt64)
	case forgetDone:
		end := time.Now()
		if at, err := time.Parse(time.RFC3339Nano, status.FinishedAt); err == nil {
			end = at
		}
		forgetQuietUntil.Store(end.Add(forgetQuietAfter).UnixMilli())
	}
}

// derivedSavesQuiet reports whether a derived build must save nothing now:
// while Forget everything runs, and for a while after, so a build that began
// before it can't write what it gathered back. Builds that carry a privacy
// token (the brief's print, the derived views' stamp, the topics revision)
// already drop on the withdrawals the job publishes; this covers the rest.
func derivedSavesQuiet() bool {
	quiet := time.Now().UnixMilli() < forgetQuietUntil.Load()
	if quiet {
		log.Printf("[forget-everything] a derived save waits for the quiet window")
	}
	return quiet
}
