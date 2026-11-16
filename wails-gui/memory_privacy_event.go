package main

// memoryWithdrawnEvent is the privacy half of a memory change: a forget, or any
// change the person made to their own memory (a correction, a delete, a
// reassignment, a hide). It is emitted just before memory:updated, with the same
// payload, and it is the event that clears every saved copy on the screens.
//
// memory:updated alone is a plain content change: an index pass or background
// learning. Screens only mark their memory reads stale for it; they do not clear
// or re-read anything. See docs/privacy-module-2026-11-15.md.
const memoryWithdrawnEvent = "memory:withdrawn"

// memoryReasonWithdraws reports whether a markMemoryDirty reason is a privacy
// action. Only background learning is content-only, so an unknown reason counts
// as a withdrawal (fail closed).
func memoryReasonWithdraws(reason string) bool { return derivedMemoryReasonIsEdit(reason) }

// emitMemoryUpdated sends memory:updated, first sending memory:withdrawn when
// the change is a privacy action.
func (a *App) emitMemoryUpdated(withdraws bool, data ...interface{}) {
	if withdraws {
		a.emitBrowserEvent(memoryWithdrawnEvent, data...)
	}
	a.emitBrowserEvent("memory:updated", data...)
}
