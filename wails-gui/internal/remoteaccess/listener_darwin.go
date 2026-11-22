//go:build darwin && cgo

package remoteaccess

/*
#include <libproc.h>
#include <mach/mach.h>
#include <sys/proc.h>

// The kernel checks the audit token's PID version when delivering the
// signal, including if the PID is reused after we inspect the arguments.
static int listener_token(pid_t pid, audit_token_t *token) {
	mach_port_t task;
	if (task_name_for_pid(mach_task_self(), pid, &task) != KERN_SUCCESS)
		return -1;
	mach_msg_type_number_t count = TASK_AUDIT_TOKEN_COUNT;
	kern_return_t kr = task_info(task, TASK_AUDIT_TOKEN, (task_info_t)token, &count);
	mach_port_deallocate(mach_task_self(), task);
	return kr == KERN_SUCCESS ? 0 : -1;
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func signalListener(r listenerRecord, sig syscall.Signal) (bool, error) {
	var token C.audit_token_t
	// Bind the signal target before looking up anything by PID.
	haveToken := sig == 0 || C.listener_token(C.pid_t(r.PID), &token) == 0
	raw, err := unix.SysctlRaw("kern.procargs2", r.PID)
	if err != nil {
		// procargs2 can return EINVAL for a process that has already exited.
		procs, lookupErr := unix.SysctlKinfoProcSlice("kern.proc.pid", r.PID)
		if lookupErr == nil && (len(procs) == 0 || procs[0].Proc.P_stat == C.SZOMB || int(procs[0].Eproc.Ucred.Uid) != os.Geteuid()) {
			return false, nil
		}
		return false, err
	}
	if len(raw) < 4 || binary.NativeEndian.Uint32(raw[:4]) != 4 {
		return false, nil
	}
	path, rest, ok := bytes.Cut(raw[4:], []byte{0})
	if !ok {
		return false, nil
	}
	fields := strings.Split(string(bytes.TrimLeft(rest, "\x00")), "\x00")
	if len(fields) < 4 || !r.matches(string(path), fields[:4], fields[4:]) {
		return false, nil
	}
	if !haveToken {
		return false, fmt.Errorf("cannot identify dumbpipe pid %d", r.PID)
	}
	if sig != 0 {
		if errno := C.proc_signal_with_audittoken(&token, C.int(sig)); errno != 0 {
			return false, syscall.Errno(errno)
		}
	}
	return true, nil
}
