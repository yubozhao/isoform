# Forget everything

Build note for Settings › Your data › Forget everything (computer-data v1.0.0 rule 6, redesign-shell rule 12), with the hub's decisions of 2026-11-15 in the side-session plan `your-data-forget-plan.md`.

## The rule

Forget everything removes everything Isoform holds about the person, each store through its own forget or withdrawal path. There is no second delete path: where a store has no forget path yet, one is added to that store first, and the job calls it. Settings, the phone pairing and the app stay. Closed Eyes stays as it was.

## The job

- One job, saved to `forget-everything.json` in the data folder: its state (running, pending, done) and each step's state.
- Each step is safe to run again. A step that succeeded is not run again after a restart; a step that failed leaves the job pending, retried later and at launch, and the page says which step waits and why.
- Steps run in the order below, so a reader never shows what a later step still holds: sources first (activity, mail, messages), then what was learned from them (memory, people, topics), then answers and copies (briefs, snapshots).
- Binding: `StartForgetEverything()` and `GetForgetEverything()`, event `your-data:forget`.

## Steps and their paths

| Step | Path | Exists |
| --- | --- | --- |
| Activity sessions, screen and calls, with their captures | per session, the capture erase used by Activity's Forget (`eraseSessionCapture`), then the session withdrawal | yes |
| Watch recordings | `DeleteWatchRecording`, each | yes |
| Meeting notes and transcripts | `DeleteMeetingNotes`, each | yes |
| File and OCR activity | `DeleteAllFileActivity`, `ResetAllOCR` | yes |
| Browser visits and site days | per browser only (`DeleteBrowserVisitsOf`); a delete-all is added | no |
| Mail copies | the mail service's clear (`clearStored`) | yes |
| Calendar events | none: a delete-all on the calendar store is added | no |
| Messages notes | `ForgetMessages` | yes |
| Voices | `ForgetVoices` | yes |
| Chat threads | `DeleteSessionData`, each; the main chat has none, so a clear is added | partly |
| Hand-offs | `ForgetWorkOnItHandoff("")` | yes |
| Assistant history | `forgetAssistantHistory` (the switch-off path) | yes |
| Memory and beliefs | `GetMemoryForgetPreview` then `ForgetMemoryEntry`, each | yes |
| Knowledge | `DeleteKnowledgeEntry`, each | yes |
| Know-how | none: a forget is added | no |
| People | `DeletePrincipal`, each, keeping the person's own self row | yes |
| Topics, the daily store, Not mine | the topics withdrawal for moments; a clear is added to the daily store | partly |
| Answers | `DeleteAnswers`, then the kept states and Not mine too | yes (#1230) |
| Work profile | none: a forget of the approved profile and drafts is added | no |
| Agent of You claims | local forget, and a new server delete of the person's profile versions | no (server) |
| Briefs and sealed copies | none: a delete of stored briefs is added; the privacy print moves | no |
| Page snapshots and saved copies | cleared by the withdrawals above | yes |

## PRs

1. The job and the steps whose paths exist.
2. The missing forget paths, each in its own store, with its own test: calendar events, browser visits, the main chat, know-how, the topics daily store, the work profile, briefs.
3. Agent of You: `DELETE /v1/owner/profile` on the server (every profile version and its dates; the owner leaves), and the job's step that calls it, pending while the server can't be reached.
4. The page: Forget everything… in red, last, with the confirmation and progress (placeholder words in en and zh-CN until design's).

## Open

- New capture while the job runs is not part of "everything before"; the job does not pause capture.
- The person's own self row in People stays, so Isoform still knows which side of a call is theirs.
