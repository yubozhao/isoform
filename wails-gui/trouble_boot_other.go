//go:build !darwin

package main

import "time"

// systemBootTime is unknown here: a stop is taken as Isoform's own.
func systemBootTime() time.Time { return time.Time{} }
