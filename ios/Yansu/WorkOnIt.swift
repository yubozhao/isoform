import SwiftUI
import UIKit

/// Work on it on the phone, v1 (docs/work-on-it-agents.md). A brief item's own words become a task the person
/// sees in full; one tap copies it and opens their agent, or shares it to any app. Which agents are installed is
/// read with canOpenURL against the short declared list below (LSApplicationQueriesSchemes), only to build this
/// menu: the answer never leaves the phone. Off unless the Work on it switch in Settings is on.
enum WorkOnIt {
    /// The UserDefaults key of the switch.
    static let enabledKey = "workOnIt.enabled"

    struct Agent: Equatable, Identifiable, Sendable {
        let id: String
        let name: String
        /// Opens the agent's app; the task goes on the clipboard first.
        let scheme: String
    }

    /// The declared list, in default order. Every scheme here is also in LSApplicationQueriesSchemes.
    static let catalog: [Agent] = [
        Agent(id: "claude", name: "Claude", scheme: "claude"),
        Agent(id: "chatgpt", name: "ChatGPT", scheme: "chatgpt"),
        Agent(id: "grok", name: "Grok", scheme: "grok"),
    ]

    /// The installed agents, in catalog order; the first is the default.
    static func installed(canOpen: (URL) -> Bool) -> [Agent] {
        catalog.filter { agent in
            guard let url = URL(string: "\(agent.scheme)://") else { return false }
            return canOpen(url)
        }
    }

    /// The task text, the same shape the computer prepares: the item's title, its detail, what done looks like,
    /// and a line asking the agent to say what it did.
    static func text(title: String, detail: String?, done: String?) -> String {
        func clean(_ value: String?) -> String {
            (value ?? "").split(whereSeparator: \.isWhitespace).joined(separator: " ")
        }
        let title = clean(title)
        let detail = clean(detail)
        let done = clean(done)
        var parts = [title]
        if !detail.isEmpty, detail != title { parts.append(detail) }
        if !done.isEmpty { parts.append(String(localized: "Done looks like: \(done)")) }
        parts.append(String(localized: "When you're done, tell me what you did."))
        return String(parts.joined(separator: "\n\n").prefix(4000))
    }
}

/// The row's "Work on it" chip and its sheet.
struct WorkOnItButton: View {
    let title: String
    let detail: String?
    let done: String?
    @AppStorage(WorkOnIt.enabledKey) private var enabled = false
    @State private var showing = false

    var body: some View {
        if enabled, !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            Button(String(localized: "Work on it")) { showing = true }
                .buttonStyle(YansuChipButtonStyle())
                .accessibilityIdentifier("brief.workOnIt")
                .sheet(isPresented: $showing) {
                    WorkOnItSheet(text: WorkOnIt.text(title: title, detail: detail, done: done),
                                  agents: WorkOnIt.installed { UIApplication.shared.canOpenURL($0) })
                        .presentationDetents([.medium, .large])
                }
        }
    }
}

struct WorkOnItSheet: View {
    let text: String
    let agents: [WorkOnIt.Agent]
    @Environment(\.dismiss) private var dismiss
    @State private var opened: WorkOnIt.Agent?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text("This is exactly what goes:")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    Text(verbatim: text)
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                        .background(RoundedRectangle(cornerRadius: 12).fill(YansuTheme.surfaceWarm))
                        .accessibilityIdentifier("workOnIt.text")
                    if let opened {
                        Text("Copied. Paste it in \(opened.name) and send.")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("workOnIt.copied")
                    }
                    ForEach(agents) { agent in
                        Button(String(localized: "Send to \(agent.name)")) { open(agent) }
                            .buttonStyle(YansuChipButtonStyle())
                            .accessibilityIdentifier("workOnIt.agent.\(agent.id)")
                    }
                    ShareLink(item: text) { Text("Share...") }
                        .accessibilityIdentifier("workOnIt.share")
                }
                .padding(20)
            }
            .navigationTitle(Text("Work on it"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                }
            }
        }
    }

    /// The tap: copy the task, then open the agent so the person pastes and sends it there.
    private func open(_ agent: WorkOnIt.Agent) {
        UIPasteboard.general.string = text
        opened = agent
        if let url = URL(string: "\(agent.scheme)://") {
            UIApplication.shared.open(url)
        }
    }
}
