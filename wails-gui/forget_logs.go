package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// forgetDumpsAndLogs removes the files outside the stores that can hold the
// person's content (Your data's Forget everything): the accessibility probe
// dumps, which keep the text of a screen's controls, and the crash logs. The
// crash log this process is writing now stays, so a crash during the job is
// still recorded. The crash output holds the runtime's panic and stacks only;
// no path writes screen, mail or chat text into it.
func (a *App) forgetDumpsAndLogs(_ context.Context) error {
	root, err := flavorDataDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(root, "ax-dumps")); err != nil {
		return err
	}
	dir := filepath.Join(root, "logs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	current := fmt.Sprintf("-%d.txt", os.Getpid())
	for _, e := range entries {
		if strings.Contains(e.Name(), current) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
