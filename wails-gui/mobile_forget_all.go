package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Forget everything on the phones (PROTOCOL.md 28). The computer's Forget
// everything job asks every paired phone to delete what it keeps for this
// computer; each phone tells its watch. For each phone the relay first drops
// everything queued for the pairing, then a sealed forget_all envelope goes,
// resent until the phone confirms with POST /forget-all/{jobId}/done. The
// pairing stays.

const (
	mobileKindForgetAll    = "forget_all"
	mobileForgetAllFile    = "forget-all.json"
	mobileForgetPending    = "pending"
	mobileForgetConfirmed  = "confirmed"
	mobileForgetUnpaired   = "unpaired"
	mobileForgetAllKeepFor = 30 * 24 * time.Hour
)

// The privacy job (forget_phones.go) declares forgetAllOnDevicesFn and
// forgetAllConfirmedHook; this side fills the first and calls the second
// once per confirm, so neither needs the other to build.
func init() {
	forgetAllOnDevicesFn = func(a *App, ctx context.Context, jobID string) ([]string, error) {
		return a.forgetAllOnDevices(ctx, jobID)
	}
}

type mobileForgetDevice struct {
	State       string `json:"state"`
	SentAt      string `json:"sentAt,omitempty"`
	ConfirmedAt string `json:"confirmedAt,omitempty"`
}

type mobileForgetJob struct {
	At      string                         `json:"at"`
	Devices map[string]*mobileForgetDevice `json:"devices"`
}

// mobileForgetAllPlaintext is the forget_all envelope's sealed body.
type mobileForgetAllPlaintext struct {
	V     int    `json:"v"`
	JobID string `json:"jobId"`
	At    string `json:"at"`
}

var (
	mobileForgetAllMu   sync.Mutex
	errForgetAllJobID   = errors.New("a forget job needs a UUID")
	errForgetAllOffline = errors.New("the phone connection is off")
)

func (bridge *mobileBridge) forgetAllPath() string {
	return filepath.Join(bridge.stateDir, mobileForgetAllFile)
}

func (bridge *mobileBridge) readForgetJobsLocked() map[string]*mobileForgetJob {
	jobs := map[string]*mobileForgetJob{}
	if bridge.stateDir == "" {
		return jobs
	}
	data, err := os.ReadFile(bridge.forgetAllPath())
	if err != nil {
		return jobs
	}
	_ = json.Unmarshal(data, &jobs)
	return jobs
}

func (bridge *mobileBridge) writeForgetJobsLocked(jobs map[string]*mobileForgetJob, now time.Time) error {
	if bridge.stateDir == "" {
		return nil
	}
	for id, job := range jobs {
		if at, err := time.Parse(time.RFC3339, job.At); err == nil && now.Sub(at) > mobileForgetAllKeepFor {
			delete(jobs, id)
		}
	}
	data, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(bridge.stateDir, mobileForgetAllFile, data)
}

// forgetAllOnDevices starts (or resends) a forget job on every paired phone
// and returns the devices still pending. A second call with the same job only
// resends to the ones that haven't confirmed.
func (a *App) forgetAllOnDevices(ctx context.Context, jobID string) ([]string, error) {
	if !mobileValidUUID(jobID) {
		return nil, errForgetAllJobID
	}
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return nil, errForgetAllOffline
	}
	now := time.Now()
	mobileForgetAllMu.Lock()
	jobs := bridge.readForgetJobsLocked()
	job := jobs[jobID]
	if job == nil {
		job = &mobileForgetJob{At: mobileTimestamp(now), Devices: map[string]*mobileForgetDevice{}}
		jobs[jobID] = job
	}
	bridge.mu.Lock()
	for id := range bridge.devices {
		if job.Devices[id] == nil {
			job.Devices[id] = &mobileForgetDevice{State: mobileForgetPending}
		}
	}
	// A phone unpaired since keeps nothing for this computer: it is done.
	for id, device := range job.Devices {
		if device.State == mobileForgetPending && bridge.devices[id] == nil {
			device.State = mobileForgetUnpaired
		}
	}
	bridge.mu.Unlock()
	pending := []string{}
	for id, device := range job.Devices {
		if device.State == mobileForgetPending {
			pending = append(pending, id)
		}
	}
	sort.Strings(pending)
	err := bridge.writeForgetJobsLocked(jobs, now)
	mobileForgetAllMu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, id := range pending {
		if err := bridge.sendForgetAll(jobID, id, now); err != nil {
			log.Printf("[mobile] forget_all waits for the relay: %v", err)
		}
	}
	return pending, nil
}

// forgetAllStatus is each device's state in a job: pending, confirmed, or
// unpaired (nothing left to forget).
func (a *App) forgetAllStatus(jobID string) map[string]string {
	out := map[string]string{}
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return out
	}
	mobileForgetAllMu.Lock()
	job := bridge.readForgetJobsLocked()[jobID]
	mobileForgetAllMu.Unlock()
	if job == nil {
		return out
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	for id, device := range job.Devices {
		state := device.State
		if state == mobileForgetPending && bridge.devices[id] == nil {
			state = mobileForgetUnpaired
		}
		out[id] = state
	}
	return out
}

// sendForgetAll tells the relay to drop what is queued for the pairing, then
// sends the sealed forget_all envelope, in that order on one connection.
func (bridge *mobileBridge) sendForgetAll(jobID, deviceID string, now time.Time) error {
	plaintext, err := json.Marshal(mobileForgetAllPlaintext{V: 1, JobID: jobID, At: mobileTimestamp(now)})
	if err != nil {
		return err
	}
	bridge.mu.Lock()
	record := bridge.devices[deviceID]
	if bridge.stopped || bridge.identity == nil || bridge.client == nil || record == nil {
		bridge.mu.Unlock()
		return errForgetAllOffline
	}
	keys, err := bridge.sessionKeysLocked(record)
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	record.SendSeq++
	env, err := sealMobileEnvelope(keys.H2D, bridge.identity.HubID, deviceID, mobileDirH2D, record.SendSeq, mobileKindForgetAll, plaintext)
	if err != nil {
		record.SendSeq--
		bridge.mu.Unlock()
		return err
	}
	if err := bridge.persistDevicesLocked(); err != nil {
		log.Printf("[mobile] could not save send counters: %v", err)
	}
	client := bridge.client
	bridge.mu.Unlock()
	if err := client.send(map[string]any{"t": "purge", "deviceId": deviceID}); err != nil {
		return err
	}
	if err := client.send(mobileEventFrame{T: "out", Env: env}); err != nil {
		return err
	}
	mobileForgetAllMu.Lock()
	jobs := bridge.readForgetJobsLocked()
	if job := jobs[jobID]; job != nil && job.Devices[deviceID] != nil {
		job.Devices[deviceID].SentAt = mobileTimestamp(now)
		_ = bridge.writeForgetJobsLocked(jobs, now)
	}
	mobileForgetAllMu.Unlock()
	return nil
}

// resendForgetAll sends every pending forget again after the relay
// registers; the phone's handler is idempotent.
func (bridge *mobileBridge) resendForgetAll() {
	mobileForgetAllMu.Lock()
	jobs := bridge.readForgetJobsLocked()
	mobileForgetAllMu.Unlock()
	now := time.Now()
	for jobID, job := range jobs {
		for deviceID, device := range job.Devices {
			if device.State == mobileForgetPending {
				_ = bridge.sendForgetAll(jobID, deviceID, now)
			}
		}
	}
}

// serveForgetAllDone answers POST /forget-all/{jobId}/done: this phone (and
// its watch) deleted what it kept for this computer.
func (bridge *mobileBridge) serveForgetAllDone(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, jobID string) {
	var input struct {
		IdempotencyKey string   `json:"idempotencyKey"`
		Forgot         []string `json:"forgot,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	now := time.Now()
	mobileForgetAllMu.Lock()
	jobs := bridge.readForgetJobsLocked()
	job := jobs[jobID]
	if job == nil || job.Devices[device.DeviceID] == nil {
		mobileForgetAllMu.Unlock()
		mobileJSON(w, http.StatusNotFound, nil, "no forget job for this phone")
		return
	}
	first := job.Devices[device.DeviceID].State != mobileForgetConfirmed
	if first {
		job.Devices[device.DeviceID].State = mobileForgetConfirmed
		job.Devices[device.DeviceID].ConfirmedAt = mobileTimestamp(now)
		if err := bridge.writeForgetJobsLocked(jobs, now); err != nil {
			mobileForgetAllMu.Unlock()
			mobileJSON(w, http.StatusServiceUnavailable, nil, "the confirm could not be kept; try again")
			return
		}
	}
	mobileForgetAllMu.Unlock()
	if first {
		log.Printf("[mobile] forget_all confirmed job=%s device=%s forgot=%v", jobID, device.DeviceID, input.Forgot)
		if forgetAllConfirmedHook != nil {
			forgetAllConfirmedHook(jobID, device.DeviceID, now)
		}
	}
	mobileJSON(w, http.StatusOK, map[string]any{"confirmed": true}, "")
}
