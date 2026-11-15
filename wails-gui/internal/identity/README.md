# identity

Identity resolution for the Yansu Desktop app. This package turns raw captured
sender strings — e.g. a Discord username or a WeChat handle — into resolved
`Principal` references so downstream pipeline stages know whether a message came
from the local user, a known contact, or an unidentified sender.

## Purpose

When the activity pipeline parses a chat message, it produces a raw triplet of
`(app, handleID, displayName)`. This package is the semantic layer that resolves
that triplet into a `SubjectRef` — a typed, storage-backed identity with enough
metadata for prompt builders, write gates, and the People page.

## Key Components

| File | Purpose |
|------|---------|
| `resolver.go` | Core `Resolver` that looks up senders against storage in priority order |
| `normalize.go` | Cleans raw sender strings by stripping parser artifacts (timestamps, role badges, etc.) |
| `chat_apps.go` | Defines which apps are "chat apps" whose senders auto-bucket into anonymous contacts |
| `app_display.go` | Maps lowercase app IDs to brand-correct display names |

### resolver.go

The `Resolver` performs a prioritized lookup chain:

1. **Handle ID** — exact `(app, handleID)` match against `app_handles.handle_id`
2. **Display Handle** — exact `(app, displayName)` match against `app_handles.display_handle`
3. **Alias** — display name match against `principals.display_name` and aliases
4. **Auto-anonymous bucket** — for chat apps, create an anonymous Contact principal on first sighting

```go
r := identity.NewResolver(store, true) // autoBucket = true for production
ref, err := r.ResolveSender(ctx, "wechat", "wxid_abc", "Alice")
// ref.Kind == identity.SubjectContact (or SubjectSelf / SubjectAnonymous / SubjectUnknown)
```

### normalize.go

Chat parsers (especially for Discord) sometimes concatenate UI chrome into the
sender field — timestamps, role badges, server tags, even message body fragments.
`NormalizeSenderName` strips these artifacts iteratively until stable:

```go
clean := identity.NormalizeSenderName("Alice OP Original Poster 12:51")
// clean == "Alice"
```

Stripped artifacts include:
- Trailing timestamps (`12:25`, `Today at 9:01 AM`, `今天 12:25`)
- Trailing bare dates (`2026/4/30`)
- Discord server tags (`Server Tag: CHRM`)
- Role badges (`OP Original Poster`, `BOT`, `Server Owner`, `App`)
- Message body bleed (sentence fragments ending in punctuation)

The function is **conservative**: if stripping would empty the string, the original
input is returned unchanged. It also folds whitespace runs (no-break, ideographic
and zero-width characters included), so "Mei  Chen" and "Mei Chen" are one person.

A stripped `BOT`/`APP` badge means the sender is software: `SenderIsAutomated`
reports it on the raw string and the sender labeler then calls
`ResolveExistingSender`, which never creates a person. `IsSystemSender`
(`system_senders.go`) lists platform and integration accounts (Slackbot,
文件传输助手, GitHub) that are never auto-bucketed and that Clean up removes.

### chat_apps.go

Only a curated set of apps are considered "chat apps" whose senders should be
auto-bucketed into anonymous contacts. Email clients (`mail`, `gmail`), service
apps (`github`, `sentry`, `notion`), and AI agents (`chatgpt`) are explicitly
excluded because their "senders" are usually newsletters, services, or brand
accounts — not real people the user wants tracked on the People page.

```go
identity.IsChatApp("wechat")   // true
identity.IsChatApp("mail")     // false
identity.IsChatApp("chatgpt")  // false
```

### app_display.go

Maps lowercase app identifiers to human-readable, brand-correct display names.
Unknown IDs fall back to title-case.

```go
identity.AppDisplayName("wechat")   // "WeChat"
identity.AppDisplayName("github")   // "GitHub"
identity.AppDisplayName("new_app")  // "New App" (fallback)
```

## Architecture

```
  Raw capture: (app, handleID, displayName)
         |
         v
  [NormalizeSenderName]  -- strips parser artifacts
         |
         v
  [Resolver.ResolveSender]
    1. ResolveByAppHandle     (app, handleID)
    2. ResolveByDisplayHandle (app, displayName)
    3. ResolveByAlias         (displayName)
    4. EnsureAnonymousContact (chat apps only, if autoBucket enabled)
         |
         v
  SubjectRef { PrincipalID, Kind, DisplayName, Relationship, Method, Confidence }
```

### SubjectKind

| Kind | Meaning |
|------|---------|
| `SubjectSelf` | The local user (the person running Yansu) |
| `SubjectContact` | A known, named contact |
| `SubjectAnonymous` | An unidentified sender, auto-bucketed for later naming |
| `SubjectUnknown` | No identifying signal could be derived |

### AttributionMethod

Each `SubjectRef` records which lookup path succeeded:

- `MethodHandleID` — matched via `(app, handleID)`
- `MethodDisplayHandle` — matched via `(app, displayName)`
- `MethodAlias` — matched via name/alias lookup
- `MethodAutoAnonymous` — auto-created anonymous bucket
- `MethodUnknown` — no match found

This is useful for telemetry and debugging mis-attributions.

## Dependencies

- `wails-gui/storage` — `PrincipalRow`, `PrincipalKind*`, and the storage interface

The `Resolver` depends on a narrow `Storage` interface (not the full `*storage.Store`):

```go
type Storage interface {
    ResolveByAppHandle(ctx context.Context, app, handleID string) (*storage.PrincipalRow, error)
    ResolveByDisplayHandle(ctx context.Context, app, displayHandle string) (*storage.PrincipalRow, error)
    ResolveByAlias(ctx context.Context, name string) (*storage.PrincipalRow, error)
    EnsureAnonymousContact(ctx context.Context, app, handleID, displayHandle string) (storage.PrincipalRow, error)
}
```

This keeps the resolver decoupled from storage internals and makes testing easy.

## Usage

### Basic resolution

```go
store, _ := storage.New("/path/to/db")
r := identity.NewResolver(store, true)

ref, err := r.ResolveSender(ctx, "discord", "uid_123", "Bob#1234")
if err != nil {
    // storage error
}
if ref.IsSelf() {
    // user's own message
}
```

### Normalization before resolution

```go
raw := "Alice OP Original Poster Today at 12:51 PM"
displayName := identity.NormalizeSenderName(raw)
// displayName == "Alice"
ref, _ := r.ResolveSender(ctx, "discord", "", displayName)
```

### Read-only resolution (no auto-bucket)

```go
r := identity.NewResolver(store, false)
ref, _ := r.ResolveSender(ctx, "slack", "U999", "Stranger")
// ref.Kind == SubjectUnknown (no new anonymous contact created)
```

## Testing

All components have comprehensive unit tests:

```bash
cd wails-gui && go test ./internal/identity/...
```

| Test file | Coverage |
|-----------|----------|
| `resolver_test.go` | Lookup ordering, auto-bucket behavior, edge cases, integration with real SQLite store |
| `normalize_test.go` | Timestamp stripping, badge stripping, CJK names, preservation of legitimate names |
| `chat_apps_test.go` | Chat vs non-chat app classification |
| `app_display_test.go` | Known app display names, fallback title-casing |

The resolver tests use a `fakeStore` in-memory stub for fast unit tests, plus
contract tests (`realstore_test.go`) that round-trip through real SQLite to catch
integration drift.
