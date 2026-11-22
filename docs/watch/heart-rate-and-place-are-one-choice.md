# Heart rate and Place are one choice on the computer, the phone and the watch (2026-11-22)

The computer keeps the two settings since #1403 (PROTOCOL.md 24.15). This is the phone and the
watch side.

## Phone

- It reads `watchHeartRate` and `watchPlace` from the computer's status and posts a change to
  `/settings/watch-heart-rate` and `/settings/watch-place`. With an older computer the fields
  are absent, the phone shows no rows, and the watch's own switches decide as before.
- The Watch tab's "Your watch" group has the two rows after Notes for long talks, with design's
  words (computer-conformance-answers v1.3.0, W1): `watch.set.heart`, `watch.set.heartSub`,
  `watch.set.place`, `watch.set.placeSub`.
- The watch is told only what someone chose (`chosen` true). Until then nothing is said and
  the watch's own switch stands, which is also what the computer does with what arrives.
- When the computer says it takes the report (`watchPairing`), the phone tells it whether a
  watch with Isoform is paired, once the system has said, and again when that changes.

## Watch

- A choice from the phone sets the watch's switch. Turned on from elsewhere it only sets the
  switch: the samplers do nothing without the wearer's permission on the watch, and the watch's
  own rows still ask for it there.
- A switch the wearer changes on the watch goes to the phone, which saves it on the computer,
  so all three show the same.
- Turning Place off forgets the area names the watch had kept.

## Where the area's name comes from

With Place on, the watch asks Apple to name the area (`CLGeocoder`), with a position rounded to
two decimals, about a kilometer, once for each new rounded position. Design's row for that is in
Your data, under "Left your devices": `data.appleArea`, "Apple, to name the area", with "Where
you were when an hour was kept, while Place is on". It shows while Place is on and for a week
after it was last on. The phone cannot see what the watch sent, so the row goes by when Place
was on.

## Not in this change

- Asking for the watch's permission "on the next raise" after a switch was turned on elsewhere.
  Today the wearer opens the row on the watch.
- The phone's line after a long time without sending (design's W4).
