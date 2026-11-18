# A refused pairing is said on the Watch tab

On 2026-11-16 the relay stopped accepting one phone's credentials (it answered 401 to every
upload). The phone kept every part and tried again, which is right, but the Watch tab only said
recordings go when the computer is awake. A day of recordings waited and nothing told the person
to pair again.

Now a 401 on a part's upload ends the pass with the outcome `pairAgain`
(`WatchRecordingForwarder.Outcome`), and the Watch tab's line above the list says to open
Settings and choose Pair again, and that the recordings stay on the phone until then.

What does not change:

- Nothing is counted against the part and nothing is deleted. The audio stays on the phone.
- The pass is tried again by itself (`waitingRetrySeconds`), and every pass reads the pairing
  fresh, so the parts go up with the new credentials after pairing, oldest first.

Guard: `testARefusedPairingIsSaidAndThePartGoesAfterPairingAgain` in
`ios/YansuTests/WatchRecordingTests.swift`.

Not done here: the upload has no other path when the relay refuses the phone. A background
upload runs while the app is closed, when the direct path is not there.
