package main

import (
	"context"
	"os"
)

// forgetWorkProfile removes the work profile kept on this computer: the
// approved profile, any draft waiting, the lines the person removed and the
// owner portrait
// (Your data's Forget everything). The copy on the Agent of You server is
// removed by its own step.
func (a *App) forgetWorkProfile(_ context.Context) error {
	path, err := workProfilePath()
	if err != nil {
		return err
	}
	workProfileStateMu.Lock()
	err = os.Remove(path)
	workProfileStateMu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// The owner portrait kept from an assistant (mcp_portrait.go) is the
	// same Agent of You data, so it goes in this step.
	if err := forgetOwnerPortrait(); err != nil {
		return err
	}
	a.workProfileChanged()
	return nil
}
