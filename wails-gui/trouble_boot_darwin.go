//go:build darwin

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// systemBootTime is when the computer started, from kern.boottime.
func systemBootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}
	}
	return time.Unix(tv.Unix())
}
