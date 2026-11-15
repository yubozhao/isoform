package main

import (
	"os"
	"strings"
)

// writeAtomic writes data to path through a temporary file and a rename.
func writeAtomic(path, data string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// shellQuotePOSIX quotes s for a POSIX shell when it needs quoting.
func shellQuotePOSIX(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`*?|&;<>(){}[]#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
