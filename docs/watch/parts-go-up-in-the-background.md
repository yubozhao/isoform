# Parts go up in the background

2026-11-17. A day of watch recordings stayed on the phone. After the relay had
room again, the phone started six uploads on 2026-11-16; five were cut in the
middle. The phone sent a part (about 3 MB) with an ordinary request, and iOS
stops such a request when the app leaves the screen. Each cut ended the phone's
pass, and the next pass came only when the app was opened again.

Since then (`ios/Yansu/RecordingBackgroundUploads.swift`,
`ios/Yansu/WatchRecordingForwarder.swift`):

1. **A part is a background upload.** The phone writes the sealed part to a
   file and hands it to iOS. iOS sends it while the app is suspended or closed,
   waits for a connection, and tries again by itself for up to 12 hours. When
   an upload ends, the phone makes a pass (iOS starts the app for that if it
   was closed).
2. **A part keeps one upload id.** The id, key and nonce are chosen and written
   down before the bytes leave, and every later try of the part uses them, so
   the same bytes go up under the same id. If an upload arrived without the
   phone hearing of it, the relay answers 409 for the next hand-over and the
   phone counts the part as arrived. No second copy uses the relay's room.
3. **A pass does not stop at a part that is not finished.** An upload still in
   flight, or a part the computer could not fetch yet, is left for the next
   pass and the pass goes on with the others. Four uploads are with iOS at a
   time, so the relay's room for the phone is not filled with parts the
   computer has not fetched.

Unchanged: a part the computer refuses (400) is sealed anew and waits its turn;
a full relay is said as it is; the audio stays on the phone until the computer
stores it or the person deletes it.

Not covered: the background session itself has no unit test (it needs iOS);
the forwarder's side is tested with a stand-in. Proof is a real phone: start a
sync, close the app, and the parts arrive on the computer.
