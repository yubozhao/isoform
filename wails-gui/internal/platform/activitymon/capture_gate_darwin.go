package activitymon

import "C"

import "sync/atomic"

// The native event tap is process-wide. Start installs its live gate before
// enabling the tap. Native callbacks consult it on the serial AX queue.
var nativeCaptureGate atomic.Pointer[func() bool]

//export activityMonitorCaptureAllowed
func activityMonitorCaptureAllowed() C.int {
	gate := nativeCaptureGate.Load()
	if gate != nil && (*gate)() {
		return 1
	}
	return 0
}
