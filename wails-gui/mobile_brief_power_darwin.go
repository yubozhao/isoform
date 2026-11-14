//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation -framework Foundation
#include <stdint.h>
#include <stdlib.h>
#include <IOKit/IOReturn.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <CoreFoundation/CoreFoundation.h>
#import <Foundation/Foundation.h>

// yansuBriefCreateSleepAssertion asks macOS to keep the system awake while
// the brief is generated. The assertion ID is written only on success.
static int yansuBriefCreateSleepAssertion(uint32_t *outID, const char *reason) {
    CFStringRef name = CFStringCreateWithCString(kCFAllocatorDefault, reason, kCFStringEncodingUTF8);
    if (name == NULL) {
        return -1;
    }
    IOPMAssertionID id = 0;
    IOReturn status = IOPMAssertionCreateWithName(
        kIOPMAssertionTypePreventUserIdleSystemSleep,
        kIOPMAssertionLevelOn,
        name,
        &id);
    CFRelease(name);
    if (status != kIOReturnSuccess) {
        return (int)status;
    }
    *outID = id;
    return 0;
}

static int yansuBriefReleaseSleepAssertion(uint32_t id) {
    return (int)IOPMAssertionRelease(id);
}

// yansuBriefBeginActivity opts the process out of App Nap while the brief is
// made. A sleep assertion alone keeps the Mac awake but not the app at full
// speed: with the screen locked, App Nap throttled the voice about twentyfold
// (the 2026-11-13 morning recording took 20 minutes instead of under one).
static void *yansuBriefBeginActivity(const char *reason) {
    @autoreleasepool {
        NSString *why = [NSString stringWithUTF8String:reason];
        if (why == nil) {
            why = @"Isoform brief";
        }
        id<NSObject> token = [[NSProcessInfo processInfo]
            beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
                              reason:why];
        return token == nil ? NULL : (void *)CFBridgingRetain(token);
    }
}

static void yansuBriefEndActivity(void *token) {
    if (token == NULL) {
        return;
    }
    @autoreleasepool {
        id<NSObject> activity = (id<NSObject>)CFBridgingRelease(token);
        [[NSProcessInfo processInfo] endActivity:activity];
    }
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// holdMobileBriefPowerAssertion keeps the Mac from idle-sleeping and the app
// out of App Nap while a brief or its recording is made. The returned release
// function drops both and is safe to call more than once.
func holdMobileBriefPowerAssertion(reason string) (func(), error) {
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))

	var id C.uint32_t
	if status := C.yansuBriefCreateSleepAssertion(&id, cReason); status != 0 {
		return nil, fmt.Errorf("sleep assertion was not granted (status %d)", int(status))
	}
	assertion := id
	activity := C.yansuBriefBeginActivity(cReason)
	var once sync.Once
	return func() {
		once.Do(func() {
			C.yansuBriefEndActivity(activity)
			_ = C.yansuBriefReleaseSleepAssertion(assertion)
		})
	}, nil
}
