package main

import (
	"context"
	"testing"
)

func TestForgetOnPhonesStaysPendingUntilEachPhoneConfirms(t *testing.T) {
	previousList, previousFn := pairedDeviceIDs, forgetAllOnDevicesFn
	t.Cleanup(func() { pairedDeviceIDs, forgetAllOnDevicesFn = previousList, previousFn })
	devices := []string{"iphone", "ipad"}
	pairedDeviceIDs = func(*App) ([]string, error) { return devices, nil }
	forgetAllOnDevicesFn = nil
	app := &App{}

	// Not built yet: a paired phone keeps the step pending.
	if err := app.forgetOnPhones(context.Background()); err == nil {
		t.Fatal("a paired phone must keep the step pending until the forget exists")
	}
	pending := []string{"iphone", "ipad"}
	var jobs []string
	forgetAllOnDevicesFn = func(_ *App, _ context.Context, jobID string) ([]string, error) {
		jobs = append(jobs, jobID)
		return pending, nil
	}
	if err := app.forgetOnPhones(context.Background()); err == nil {
		t.Fatal("pending phones keep the step pending")
	}
	pending = nil
	if err := app.forgetOnPhones(context.Background()); err != nil {
		t.Fatalf("every phone confirmed: %v", err)
	}
	if len(jobs) != 2 || jobs[0] != jobs[1] {
		t.Fatalf("each run must use the same job id: %v", jobs)
	}
	// No phone paired and nothing built: nothing to wait for.
	devices, forgetAllOnDevicesFn = nil, nil
	if err := app.forgetOnPhones(context.Background()); err != nil {
		t.Fatal(err)
	}
}
