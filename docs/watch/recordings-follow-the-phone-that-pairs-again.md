# Recordings follow the phone that pairs again

On 2026-11-18 a phone paired again and got a new device id. The computer held the first parts of
its watch recordings under the old id, and it only lets the device that started a recording add
to it. Every part that followed was answered 400 "the recording is not valid", so the hours
recorded before the re-pair could not be completed. The phone also no longer saw where its
earlier recordings were.

Now a recording whose device is no longer paired passes to the paired phone that names it:

- by sending a part of it (`POST /recordings`), or
- by asking for it by id (`GET /recordings?ids=`).

The id is a random UUID that only the phone that made the recording holds, so naming it is the
proof. The recording's continuation rows pass with it. Nothing passes while the old device is
still paired, to a device that is not paired, or to a phone that only lists without ids.

Code: `mobileRecordingStore.claimLocked` in `wails-gui/mobile_recordings.go`; the bridge tells the
store which devices are paired each time it hands the store out (`recordingStoreLocked`).

Guards: `TestARecordingPassesToThePhoneThatPairedAgain` and
`TestListingByIDPassesARecordingOfAnUnpairedDevice` in `wails-gui/mobile_recording_repair_test.go`.

Not covered: the Watch tab's conversations list reads by device, so conversations from before the
re-pair show again only once the phone has named their recordings.
