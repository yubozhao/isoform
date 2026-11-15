package privacy

import (
	"strings"
	"testing"
)

func TestReadStampFormat(t *testing.T) {
	if got, want := ReadStamp(7, true, 3), "read|7|true|3"; got != want {
		t.Fatalf("stamp=%q, want %q", got, want)
	}
}

func TestReadStampChangesWithEveryInput(t *testing.T) {
	base := ReadStamp(1, false, 1)
	for name, other := range map[string]string{
		"epoch":         ReadStamp(2, false, 1),
		"closed eyes":   ReadStamp(1, true, 1),
		"excluded apps": ReadStamp(1, false, 2),
	} {
		if other == base {
			t.Fatalf("%s change kept the stamp %q", name, base)
		}
	}
}

func TestReadStampEpochPrefix(t *testing.T) {
	// The first chat page drops copies by the "read|<epoch>|" prefix alone.
	if !strings.HasPrefix(ReadStamp(42, true, 9), "read|42|") {
		t.Fatal("stamp does not start with the epoch prefix")
	}
}
