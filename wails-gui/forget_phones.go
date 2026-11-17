package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Forget everything reaches every paired phone, and through it the watch:
// each one is sent a forget, erases what it keeps (briefs, answers waiting to
// send, recordings, watch audio), keeps the pairing, and confirms. A phone
// that hasn't confirmed keeps the step pending, the same as the Agent of You
// server: an unreachable phone is sent the forget again when it reconnects.
//
// The relay, the envelope (forget_all) and the computer side of sending it
// are local_12b6156e's: they set forgetAllOnDevicesFn in their init. Until
// then a paired phone leaves the step pending, said plainly.

// forgetAllOnDevicesFn starts the forget on every paired device for jobID and
// returns the devices still pending. Called again with the same jobID it
// resends only to those. A device unpaired meanwhile counts as done.
var forgetAllOnDevicesFn func(a *App, ctx context.Context, jobID string) (pending []string, err error)

// forgetAllConfirmedHook is called once per device when its phone confirms:
// the job runs again at once, so the step finishes without waiting.
var forgetAllConfirmedHook func(jobID, deviceID string, at time.Time)

func init() {
	forgetAllConfirmedHook = func(jobID, deviceID string, _ time.Time) {
		if app := forgetEverythingApp.Load(); app != nil {
			app.runForgetEverythingSoon(0)
		}
	}
}

var errPhoneForgetNotBuilt = errors.New("forgetting on the phone isn't available yet")

// pairedDeviceIDs lists the phones paired now; tests replace it.
var pairedDeviceIDs = func(a *App) ([]string, error) {
	status, err := a.GetMobileConnectionStatus()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(status.Devices))
	for _, d := range status.Devices {
		ids = append(ids, d.DeviceID)
	}
	return ids, nil
}

// forgetOnPhones is done once no paired device is pending.
func (a *App) forgetOnPhones(ctx context.Context) error {
	if forgetAllOnDevicesFn == nil {
		ids, err := pairedDeviceIDs(a)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return fmt.Errorf("%d phone(s) not confirmed: %w", len(ids), errPhoneForgetNotBuilt)
	}
	pending, err := forgetAllOnDevicesFn(a, ctx, loadForgetEverything().JobID)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("%d phone(s) not confirmed yet", len(pending))
	}
	return nil
}
