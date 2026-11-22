//go:build !linux && (!darwin || !cgo)

package remoteaccess

const listenerRecoveryAvailable = false

// Without an identity-bound signal API, leave the recorded PID alone.
func stopRecordedListener(listenerRecord) error { return nil }
