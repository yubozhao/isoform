# wails-gui/cmd

Standalone developer tools for diagnosing and testing subsystems of the Yansu desktop app. These are not part of the main Wails build — each is a separate Go program run with `go run`.

## Tools

### ax-tree-dump

Dumps the macOS accessibility (AX) tree of the frontmost window, along with parser dispatch metadata. Used to debug why an app-specific content parser (Slack, Notion, ChatGPT, etc.) fails on a real-world window.

```
go run ./cmd/ax-tree-dump --delay 5 > /tmp/slack-axtree.json
# switch to the target app within 5 seconds
```

| Flag | Description |
|---|---|
| `--delay N` | Sleep N seconds before capture (time to focus target app) |
| `--out PATH` | Write JSON to PATH instead of stdout |
| `--no-tree` | Omit the full AX tree, keep only metadata and parser result |
| `--bundle ID` | Bypass frontmost — read AX tree of a specific app by bundle ID |
| `--list-windows` | With `--bundle`: list all top-level windows (role, title, tree size) |

Output is a JSON object containing timestamp, bundle/app info, raw AX tree (unless `--no-tree`), flat text fallback, parser result, and a dispatch summary showing which parser matched and how many messages/items were extracted.

### topic-proposal-replay

Measures the daily topics' proposals on a data folder: how many were answered, how many taken, why the rest were turned down, and how many of those the build's rest rules (`internal/topicday/rest.go`) would have left out. Read-only; prints counts only, never a title, a name, an id or a path.

```
go run ./cmd/topic-proposal-replay                    # ~/.isoform-agent
go run ./cmd/topic-proposal-replay -data /path/to/data-root
```

Output: `answered`, `yes`, `no`, `not_mine` and `accept_before`; the No reasons; the answers each rule would have rested; and `accept_after` over the proposals the rules would still have made.

### pii-test

End-to-end test of the PII detection and masking pipeline. Takes a desktop screenshot (JPEG snapshot), runs OCR, detects PII via GLiNER and regex fallback, merges detections, and produces a masked output image.

```
go run ./cmd/pii-test [path/to/snapshot.jpg]
```

If no path is given, the tool finds the most recent snapshot under `~/.yansu-agent/activity/snapshots/`.

Pipeline steps:

1. **OCR** — Apple Vision OCR via `internal/platform/ocr` (en-US + zh-Hans, mid priority)
2. **GLiNER detection** — AI-based PII classifier via `internal/pii`
3. **Regex fallback** — pattern-based detection for structured PII (emails, phone numbers, etc.)
4. **Merge** — combines GLiNER and regex results, deduplicating overlapping spans
5. **Mask** — redacts detected regions in the JPEG, writes to `/tmp/pii-masked-test.jpg`

### sherpa-bench

Benchmarks Sherpa-ONNX offline speech recognition (SenseVoice model) against a WAV file. Optionally uses Silero Voice Activity Detection for segmentation before decoding.

```
go run -tags darwin ./cmd/sherpa-bench -wav /path/to/audio.wav
```

| Flag | Description |
|---|---|
| `--wav PATH` | Path to a single-channel WAV file (required) |
| `--model-dir PATH` | Directory containing sense-voice and silero_vad models (default: `~/.yansu-agent/sherpa`) |
| `--vad` | Enable Silero VAD segmentation before decoding |
| `--lang auto` | SenseVoice language hint: `auto`, `zh`, `en`, `ja`, `ko`, `yue` |

Output includes recognized text (with SenseVoice control tags stripped), detected language, emotion, audio event labels, model load time, and Real-Time Factor (RTF).

Requires model files:
- `sense-voice/model.int8.onnx`
- `sense-voice/tokens.txt`
- `silero_vad.onnx`

## Relationship to the main app

These tools import from `wails-gui/internal/` packages (OCR, PII detection, activity monitor, app parsers, Sherpa ASR bindings). They exercise the same code paths used by the running desktop app but in an isolated, CLI-friendly context for debugging and benchmarking.

The main Wails application entry point lives at `wails-gui/main.go` — it is not in this directory.
