package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// forgetAgentOfYouProfile sends the Agent of You server the person's delete
// (Your data's Forget everything): every version of their profile is emptied
// there, and their agent stops serving. Someone who never used Agent of You
// has nothing there to delete. While the server can't be reached the step
// fails with why, so the job stays pending and tries again.
func (a *App) forgetAgentOfYouProfile(ctx context.Context) error {
	env, err := a.agentOwnerEnv()
	if err != nil {
		return err
	}
	return forgetAgentOfYouProfileWith(ctx, env)
}

func forgetAgentOfYouProfileWith(ctx context.Context, env agentOwnerEnv) error {
	if _, err := os.Stat(env.reservation.stateFile); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	state, err := loadYourAgentState(env.reservation)
	if err != nil {
		return err
	}
	if _, err := env.client.do(ctx, http.MethodDelete, "/v1/owner/profile", nil, state.Hub, state.Token); err != nil {
		return fmt.Errorf("the Agent of You server didn't take the delete yet: %w", err)
	}
	agentOwnerInvalidate(env, state.Hub, "status", "profile")
	return nil
}
