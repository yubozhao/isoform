package brief

import (
	"context"
	"errors"
	"fmt"
)

// ErrClosedEyes is returned when Closed Eyes is on. Nothing is read or composed.
var ErrClosedEyes = errors.New("brief: closed eyes is on")

// ErrBudgetLow is returned when the model budget is too low for a shadow run.
// The run is skipped before any source is read.
var ErrBudgetLow = errors.New("brief: model budget is low")

// Gather takes one immutable snapshot of the sources for w through deps and
// runs the Closed Eyes check. The privacy gates run inside deps.Snapshot,
// once, so compose never reads a source the gates have not seen.
func Gather(ctx context.Context, deps Deps, w Window) (Inputs, error) {
	if deps.ClosedEyes() {
		return Inputs{}, ErrClosedEyes
	}
	if err := ctx.Err(); err != nil {
		return Inputs{}, err
	}
	in, err := deps.Snapshot(ctx, w)
	if err != nil {
		// The app's error text can name a store or a model, so only the stage
		// is kept. Callers log the stage, never the text.
		return Inputs{}, fmt.Errorf("gather: %w", err)
	}
	if in.Language == "" {
		in.Language = deps.Language()
	}
	return in, nil
}
