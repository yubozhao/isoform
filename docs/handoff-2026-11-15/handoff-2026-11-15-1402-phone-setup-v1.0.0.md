# Phone setup v1.0.0: pair in one scan, then the latest brief at once; a lost pairing never blanks Today

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file is the first run, pairing, the one permission asked, and what Today shows when the pairing is lost.

**Board:** Isoform iPhone, artboard 9 (First run and pairing).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Paired in one scan;** nothing asked before it.
2. **The latest brief, at once,** as the first thing after pairing.
3. **Permissions asked when they're needed,** once, by saying what they're for.
4. **A lost pairing never blanks Today.**

**Before:** FirstRunView and PairingView on `feat/iphone-companion` at c1f0e8bcf (the first-run card with Not now; "Pair with your computer"; "This phone's pairing couldn't be read. Pair again." in place of Today).

## First run

1. **One screen:** the mark at 84 points, "Isoform on your phone", "Your brief, the work it found for you to judge, and your watch. All from your computer.", **Pair with your computer**, and under it "On your computer, open Isoform's Phone page and choose Pair a phone."
2. **No tour, no account, no permission** before pairing.

## Pairing

3. **Pair with your computer asks for the camera then,** with the system's prompt; "Isoform uses the camera only to read the code on your computer." as the prompt's reason.
4. **The scan screen:** "Pair with your computer" at the top, Cancel at the left, the frame, and "Point at the code on your computer's Phone page."
5. **When it can't pair, one line says why and what to do:**
   - "Update Isoform on your computer, then scan again."
   - "This code has expired. Show a new one on your computer's Phone page."
   - "Your computer couldn't be reached. Keep Isoform open on it and try again."

## After pairing

6. **"Paired with {computer}"** for two seconds, above the tabs.
7. **Today opens on the computer's latest brief,** sent with the pairing (B12): rank 1 from the first minute. With no brief made yet: "Your first brief comes at 7:00 AM."
8. **Notifications are asked under the brief, once, by saying what they are:** "Get your brief when it's ready?" / "Three kinds only: the brief, meeting prep 15 minutes before, and work that finished while you were away." with **Turn on** (then the system's prompt) and **Not now** (asked again only from Settings › Notifications).
9. **The watch** isn't part of setup: its tab appears when a watch is paired and offers Install there (phone-watch v1.0.0, rule 15).
10. **The microphone** is asked only when the person records their voice (Settings › Your voice).

## A lost pairing

11. **Today keeps the saved brief and Your work,** with one amber line above the brief: "This phone needs pairing again to get new briefs." and **Pair**. The build's full-page "This phone's pairing couldn't be read. Pair again." goes.
12. **Answers given meanwhile wait on this phone** ("waits for your computer") and go once paired again.
13. **Chat** shows its thread, with the field reading "Pair again to ask your computer" until paired.

## What the build needs

B12 (the latest brief package sent with the pairing), B1 (the saved brief kept through a lost pairing), B3 (answers queued while unpaired).

## Every word

| Key | English | Where |
| --- | --- | --- |
| setup.title | Isoform on your phone | First run |
| setup.body | Your brief, the work it found for you to judge, and your watch. All from your computer. | First run |
| setup.pair | Pair with your computer | Button, scan title |
| setup.where | On your computer, open Isoform's Phone page and choose Pair a phone. | First run |
| setup.camera | Isoform uses the camera only to read the code on your computer. | Camera prompt reason |
| setup.point | Point at the code on your computer's Phone page. | Scan |
| setup.update | Update Isoform on your computer, then scan again. | Scan, failed |
| setup.expired | This code has expired. Show a new one on your computer's Phone page. | Scan, failed |
| setup.unreachable | Your computer couldn't be reached. Keep Isoform open on it and try again. | Scan, failed |
| setup.paired | Paired with {computer} | Toast |
| setup.notifTitle | Get your brief when it's ready? | Under the brief |
| setup.notifBody | Three kinds only: the brief, meeting prep 15 minutes before, and work that finished while you were away. | Under the brief |
| setup.notifOn | Turn on | Button |
| setup.notifLater | Not now | Button |
| setup.lost | This phone needs pairing again to get new briefs. | Today, amber line |
| setup.lostPair | Pair | Today, amber line |
| setup.chatLost | Pair again to ask your computer | Chat field |

## Proof

On a fresh install on Bo's iPhone, a named TestFlight build:

1. **One scan:** from first launch to Today with the latest brief in under 30 seconds, nothing asked before Pair; a screen recording.
2. **The brief first:** the first page after pairing shows the computer's latest brief, under its time, and it plays.
3. **One question:** notifications are asked once, under the brief; no other permission appears in setup.
4. **Lost, not blank:** with the pairing removed on the computer, Today still shows the saved brief with the amber line and Pair; a still.

## On the map (phone)

- P241 · First run: one screen, Pair with your computer; the camera asked at Pair; one line for each failure.
- P242 · After pairing, Today opens on the latest brief, sent with the pairing; notifications asked under it, once.
- P243 · A lost pairing keeps the saved brief and Your work, with one amber line and Pair.
