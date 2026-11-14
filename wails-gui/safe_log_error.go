package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"syscall"

	"wails-gui/storage"

	"modernc.org/sqlite"
)

// codeError is an error whose text is a constant written in this code base,
// never user content, so the log may quote it.
type codeError string

func (e codeError) Error() string { return string(e) }

// logStageError names the step that failed. The stage is code text; Error()
// keeps the usual chain so callers and errors.Is see the cause unchanged.
type logStageError struct {
	stage string
	err   error
}

func (e *logStageError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *logStageError) Unwrap() error { return e.err }

func withLogStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &logStageError{stage: stage, err: err}
}

// Sentinels whose messages are code constants.
var safeLogSentinels = []error{
	context.Canceled, context.DeadlineExceeded, sql.ErrNoRows, sql.ErrConnDone, sql.ErrTxDone,
	storage.ErrReadRevisionChanged, errMobileBriefSourcesChanged, errMobileBriefStateUnavailable,
	errMobileBriefTimingDeferred, errTopicRegistryChanged,
	fs.ErrNotExist, fs.ErrPermission, fs.ErrClosed,
}

// safeLogError renders err for the log: stage names, code-constant messages,
// SQLite result codes, errno names and Go types. Free-form text from wrapped
// errors (file paths, model replies, mail or names) is never printed.
func safeLogError(err error) string {
	if err == nil {
		return "no error"
	}
	var parts []string
	for err != nil {
		var next error
		switch e := err.(type) {
		case *logStageError:
			parts = append(parts, e.stage)
			next = e.err
		case codeError:
			return strings.Join(append(parts, string(e)), ": ")
		case *sqlite.Error:
			return strings.Join(append(parts, fmt.Sprintf("sqlite result code %d", e.Code())), ": ")
		case *fs.PathError:
			parts = append(parts, "file "+e.Op)
			next = e.Err
		case syscall.Errno:
			return strings.Join(append(parts, "errno "+e.Error()), ": ")
		case *exec.ExitError:
			return strings.Join(append(parts, fmt.Sprintf("process exit code %d", e.ExitCode())), ": ")
		case *json.SyntaxError:
			return strings.Join(append(parts, fmt.Sprintf("json syntax error at offset %d", e.Offset)), ": ")
		case *json.UnmarshalTypeError:
			return strings.Join(append(parts, "json type mismatch"), ": ")
		default:
			for _, sentinel := range safeLogSentinels {
				if err == sentinel {
					return strings.Join(append(parts, sentinel.Error()), ": ")
				}
			}
			switch u := err.(type) {
			case interface{ Unwrap() error }:
				parts = append(parts, fmt.Sprintf("%T", err))
				next = u.Unwrap()
			case interface{ Unwrap() []error }:
				joined := make([]string, 0, len(u.Unwrap()))
				for _, inner := range u.Unwrap() {
					joined = append(joined, safeLogError(inner))
				}
				return strings.Join(append(parts, "["+strings.Join(joined, "; ")+"]"), ": ")
			default:
				return strings.Join(append(parts, fmt.Sprintf("unlisted %T", err)), ": ")
			}
		}
		err = next
	}
	return strings.Join(parts, ": ")
}
