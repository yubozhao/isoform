import SwiftUI

/// One "what Isoform is doing" line from the computer (PROTOCOL.md 20.7e).
struct MobilePresence: Equatable, Sendable {
    let text: [String: String]
    let at: Date

    /// Shown only while true: the computer repeats it every few seconds, so an older line is stale.
    func isFresh(now: Date = Date()) -> Bool { now.timeIntervalSince(at) < 10 }

    func line(locale: Locale) -> String? {
        text[ChatPresentationLanguage(locale: locale).code] ?? text["en"]
    }
}

struct MobilePresenceEvent: Decodable, Sendable {
    let event: String
    let text: [String: String]?
    let at: String
}

/// What Isoform sees, by job (PROTOCOL.md 20.7e).
struct MobileSees: Decodable, Equatable, Sendable {
    struct Source: Decodable, Equatable, Sendable, Identifiable {
        let job: String
        let on: Bool
        var id: String { job }
    }

    let sources: [Source]
    var assistant: String? = nil
    /// True while Agent of You acts without the person's review: the one exception to "Only with your tap"
    /// (phone-duo v1.1.0). Older computers omit it and the exception line stays hidden.
    var agentOfYouReviewOff: Bool? = nil
}

struct MobileSeesShowRequest: Encodable, Sendable {
    let idempotencyKey: String
    let job: String
}

/// What the sheet reads and asks of the computer; the UI tests seed it.
protocol SeesHubAPI: Sendable {
    func sees() async throws -> MobileSees
    func showOnComputer(job: String) async throws
}

extension HubChannel: SeesHubAPI {
    func sees() async throws -> MobileSees {
        try await perform(.privacy).payload(MobileSees.self)
    }

    func showOnComputer(job: String) async throws {
        let body = try JSONEncoder().encode(MobileSeesShowRequest(idempotencyKey: UUID().uuidString.lowercased(), job: job))
        _ = try await perform(.write(path: "/privacy/show", body: String(decoding: body, as: UTF8.self)))
    }
}

enum IsoformSeesCopy {
    static func title(_ job: String, assistant: String?) -> String {
        switch job {
        case "mail": return String(localized: "source.mail")
        case "calendar": return String(localized: "sees.calendar")
        case "calls": return String(localized: "sees.calls")
        case "screen": return String(localized: "sees.screen")
        case "assistant": return assistant == "codex" ? String(localized: "Codex") : String(localized: "Claude Code")
        default: return String(localized: "sees.contacts")
        }
    }

    static func detail(_ source: MobileSees.Source) -> String {
        switch (source.job, source.on) {
        case ("mail", true): return String(localized: "sees.mail.on")
        case ("mail", false): return String(localized: "sees.mail.off")
        case ("calendar", true): return String(localized: "sees.calendar.on")
        case ("calendar", false): return String(localized: "sees.calendar.off")
        case ("calls", true): return String(localized: "sees.calls.on")
        case ("calls", false): return String(localized: "sees.calls.off")
        case ("screen", true): return String(localized: "sees.screen.on")
        case ("screen", false): return String(localized: "sees.screen.off")
        case ("assistant", _): return String(localized: "sees.assistant.on")
        case (_, true): return String(localized: "sees.contacts.on")
        default: return String(localized: "sees.contacts.off")
        }
    }

    static func icon(_ job: String) -> (String, Color) {
        switch job {
        case "mail": return ("envelope", Color(red: 0.25, green: 0.51, blue: 0.95))
        case "calendar": return ("calendar", Color(red: 0.92, green: 0.34, blue: 0.33))
        case "calls": return ("waveform", Color(red: 0.49, green: 0.38, blue: 0.93))
        case "screen": return ("display", Color(red: 0.22, green: 0.64, blue: 0.53))
        case "assistant": return ("apple.terminal", Color(red: 0.82, green: 0.43, blue: 0.24))
        default: return ("person", Color(red: 0.55, green: 0.56, blue: 0.62))
        }
    }

    static func aiLine(_ assistant: String?) -> String {
        switch assistant {
        case "claude_code": return String(localized: "sees.leaves.ai.claude")
        case "codex": return String(localized: "sees.leaves.ai.codex")
        default: return String(localized: "sees.leaves.ai.isoform")
        }
    }

    /// "Isoform doesn't send, delete or accept anything until you tap.", with its one exception while it is so.
    static func tapLine(agentOfYouReviewOff: Bool?) -> String {
        let line = String(localized: "sees.tap.line")
        guard agentOfYouReviewOff == true else { return line }
        return line + " " + String(localized: "sees.tap.exception")
    }
}

/// Isoform's mark. Its eye blinks while Isoform works on something. Chat no longer has it (phone-duo v1.1.0 rule 4).
struct IsoformMarkButton: View {
    let working: Bool
    let action: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Button(action: action) {
            TimelineView(.animation(minimumInterval: 1 / 30, paused: !working || reduceMotion)) { timeline in
                let phase = timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 2.4)
                let blink = working && !reduceMotion && phase > 2.2 ? 0.15 : 1
                ZStack {
                    Circle().fill(YansuTheme.accentWash)
                    Circle()
                        .fill(YansuTheme.accent)
                        .frame(width: 13, height: 13)
                        .scaleEffect(x: 1, y: blink)
                        .offset(x: -3, y: -2)
                    Circle()
                        .fill(YansuTheme.accent.opacity(0.45))
                        .frame(width: 9, height: 9)
                        .offset(x: 6, y: 5)
                }
                .frame(width: 34, height: 34)
            }
            .frame(width: 44, height: 44)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "What Isoform sees"))
        .accessibilityIdentifier("chat.mark")
    }
}

/// The line at the end of the conversation while Isoform works, where the reply will appear: "Writing the reply to
/// Priya…". Only while true, in both layouts (phone-duo v1.1.0 rule 5).
struct IsoformPresenceLine: View {
    let presence: MobilePresence?
    @Environment(\.locale) private var locale

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { timeline in
            if let presence, presence.isFresh(now: timeline.date), let line = presence.line(locale: locale) {
                HStack(spacing: 8) {
                    Image(systemName: "sparkle")
                        .font(.footnote.weight(.semibold))
                        .accessibilityHidden(true)
                    Text(line)
                        .font(.subheadline.weight(.medium))
                        .lineLimit(1)
                }
                .foregroundStyle(YansuTheme.accent)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity, alignment: .leading)
                .transition(.opacity)
                .accessibilityIdentifier("chat.presence")
            }
        }
    }
}

/// What Isoform sees, on your computer, by job: on, or what you lose and Turn on. A page inside Settings › Your data,
/// the one place it opens from in both layouts (phone-duo v1.1.0 rules 1 and 3).
struct IsoformSeesPage: View {
    let hub: SeesHubAPI?
    @State private var sees: MobileSees?
    @State private var failed = false
    @State private var sent: Set<String> = []
    @State private var sending: Set<String> = []

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                Text(String(localized: "sees.subtitle"))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.bottom, 8)
                if let sees {
                    group(rows: sees.sources.map { AnyView(row($0, assistant: sees.assistant)) })
                        .accessibilityIdentifier("sees.sources")
                    header(String(localized: "sees.leaves.title"))
                    group(rows: [
                        AnyView(note("arrow.up.right", IsoformSeesCopy.aiLine(sees.assistant))),
                        AnyView(note("lock", String(localized: "sees.leaves.phone"))),
                        AnyView(note("shield", String(localized: "sees.leaves.kept")))
                    ])
                    header(String(localized: "sees.tap.title"))
                    group(rows: [AnyView(note("hand.raised",
                                              IsoformSeesCopy.tapLine(agentOfYouReviewOff: sees.agentOfYouReviewOff)))])
                    Text(String(localized: "sees.foot"))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .padding(.horizontal, 4)
                        .padding(.top, 4)
                } else if failed {
                    NoticeBanner(message: String(localized: "Couldn't reach your computer"))
                } else {
                    RoundedRectangle(cornerRadius: 18).fill(YansuTheme.surface).frame(height: 420)
                        .accessibilityHidden(true)
                }
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.bottom, 24)
        }
        .background(YansuTheme.background.ignoresSafeArea())
        .navigationTitle(String(localized: "What Isoform sees"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .accessibilityIdentifier("sees.page")
    }

    private func load() async {
        guard let hub else { failed = true; return }
        do { sees = try await hub.sees() } catch { failed = true }
    }

    private func row(_ source: MobileSees.Source, assistant: String?) -> some View {
        let (icon, tint) = IsoformSeesCopy.icon(source.job)
        return HStack(alignment: .center, spacing: 14) {
            Image(systemName: icon)
                .font(.system(size: 17, weight: .semibold))
                .foregroundStyle(.white)
                .frame(width: 34, height: 34)
                .background(tint, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                Text(IsoformSeesCopy.title(source.job, assistant: assistant))
                    .font(.body.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                Text(IsoformSeesCopy.detail(source))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if source.on {
                Label(String(localized: "sees.on"), systemImage: "checkmark")
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .labelStyle(SeesOnLabelStyle())
            } else if sent.contains(source.job) {
                Text(String(localized: "Sent to your computer"))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .multilineTextAlignment(.trailing)
                    .frame(maxWidth: 90)
                    .accessibilityIdentifier("sees.sent.\(source.job)")
            } else {
                Button(String(localized: "sees.turnOn")) { turnOn(source.job) }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .padding(.horizontal, 14)
                    .frame(minHeight: 36)
                    .overlay(Capsule().stroke(YansuTheme.borderStrong, lineWidth: 1))
                    .disabled(sending.contains(source.job))
                    .accessibilityIdentifier("sees.turnOn.\(source.job)")
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 13)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("sees.row.\(source.job)")
    }

    private func turnOn(_ job: String) {
        guard let hub else { return }
        sending.insert(job)
        Task {
            if (try? await hub.showOnComputer(job: job)) != nil {
                sent.insert(job)
            }
            sending.remove(job)
        }
    }

    private func note(_ icon: String, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Image(systemName: icon)
                .font(.subheadline)
                .foregroundStyle(YansuTheme.accent)
                .frame(width: 20)
                .accessibilityHidden(true)
            Text(text)
                .font(.body)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 13)
    }

    private func header(_ text: String) -> some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .tracking(0.8)
            .textCase(.uppercase)
            .foregroundStyle(YansuTheme.secondary)
            .padding(.horizontal, 4)
            .padding(.top, 14)
    }

    private func group(rows: [AnyView]) -> some View {
        VStack(spacing: 0) {
            ForEach(Array(rows.enumerated()), id: \.offset) { index, row in
                row
                if index < rows.count - 1 {
                    Divider().overlay(YansuTheme.border).padding(.leading, 64)
                }
            }
        }
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(YansuTheme.border, lineWidth: 1))
    }
}

private struct SeesOnLabelStyle: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 4) {
            configuration.icon.font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.accent)
            configuration.title
        }
    }
}
