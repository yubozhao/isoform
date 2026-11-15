package privacy

import "fmt"

// ReadStamp is the cheap privacy stamp a saved copy (the finds view, the
// people view, the first chat page) is served under. It names only values
// already in memory: the durable privacy epoch, Closed Eyes and the
// excluded-app version. A copy whose stamp no longer matches is not shown.
//
// The format is "read|<epoch>|<closed eyes>|<excluded apps>"; readers that
// only compare the epoch match the "read|<epoch>|" prefix, so the order of
// the fields must not change.
func ReadStamp(epoch uint64, closedEyes bool, excludedAppsVersion uint64) string {
	return fmt.Sprintf("read|%d|%t|%d", epoch, closedEyes, excludedAppsVersion)
}
