package main

import (
	"context"
	"os"
)

// forgetWorkProfile removes the work profile kept on this computer: the
// approved profile, any draft waiting, and the lines the person removed
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
	a.workProfileChanged()
	return nil
}
