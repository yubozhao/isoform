//go:build offfeatures && darwin

// Feature being deleted (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import "testing"

func TestCuaDriverSemverAtLeast(t *testing.T) {
	cases := []struct {
		version string
		min     string
		want    bool
	}{
		{version: "0.2.0", min: "0.2.0", want: true},
		{version: "0.2.1", min: "0.2.0", want: true},
		{version: "v0.3.0", min: "0.2.0", want: true},
		{version: "0.1.4", min: "0.2.0", want: false},
		{version: "not-a-version", min: "0.2.0", want: false},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			if got := semverAtLeast(c.version, c.min); got != c.want {
				t.Fatalf("semverAtLeast(%q, %q) = %v, want %v", c.version, c.min, got, c.want)
			}
		})
	}
}
