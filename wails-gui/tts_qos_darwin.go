//go:build darwin

package main

/*
#include <pthread.h>
#include <sys/qos.h>

static qos_class_t yansuTTSQoSSelf(void) { return qos_class_self(); }
static int yansuTTSSetQoS(qos_class_t class) { return pthread_set_qos_class_self_np(class, 0); }
static qos_class_t yansuTTSUtility(void) { return QOS_CLASS_UTILITY; }
*/
import "C"

import "runtime"

// runAtUtilityQoS runs fn on one OS thread at utility QoS and puts the
// thread's class back afterwards. The calling thread's class decides how fast
// Kokoro runs (measured 2026-11-13; the class the engine was created under
// made no difference), so this lowers only the work it wraps: the brief's
// recording yields to the window and chat, and chat read-aloud keeps its speed.
func runAtUtilityQoS(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := C.yansuTTSQoSSelf()
	lowered := C.yansuTTSSetQoS(C.yansuTTSUtility()) == 0
	defer func() {
		if lowered {
			_ = C.yansuTTSSetQoS(previous)
		}
	}()
	fn()
}

// currentThreadQoSClass is the calling thread's QoS class (tests).
func currentThreadQoSClass() uint32 { return uint32(C.yansuTTSQoSSelf()) }

// utilityQoSClass is QOS_CLASS_UTILITY (tests).
func utilityQoSClass() uint32 { return uint32(C.yansuTTSUtility()) }
