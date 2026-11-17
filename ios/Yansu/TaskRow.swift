import SwiftUI

// A task row and its progress ring, used by Today's Your work rows and the task thread. They outlived the Tasks
// tab (phone-today rule 19).
struct TaskRow: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.locale) private var locale
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    let task: MobileTask
    var onSelect: (() -> Void)?
    var directTasks: TasksStore?
    var source: TaskThreadSource = .tasks
    /// A row inside a grouped card (Done this week) draws no card of its own.
    var grouped = false
    /// A trailing control in place of the chevron, such as Undo on a task that just started.
    var trailing: AnyView?
    var lineOverride: String?

    init(task: MobileTask, onSelect: (() -> Void)? = nil, compact: Bool = false, directTasks: TasksStore? = nil,
         source: TaskThreadSource = .tasks, grouped: Bool = false, trailing: AnyView? = nil,
         lineOverride: String? = nil) {
        self.task = task
        self.onSelect = onSelect
        self.compact = compact
        self.directTasks = directTasks
        self.source = source
        self.grouped = grouped
        self.trailing = trailing
        self.lineOverride = lineOverride
    }

    private var compact: Bool

    @ViewBuilder
    var body: some View {
        if trailing != nil {
            rowContent
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("tasks.row.\(task.id)")
        } else if let onSelect {
            Button(action: onSelect) {
                rowContent
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("tasks.row.\(task.id)")
        } else if let directTasks {
            NavigationLink {
                TaskThreadView(task: task, tasks: directTasks, source: source)
            } label: { rowContent }
            .buttonStyle(.plain)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("tasks.row.\(task.id)")
        } else {
            NavigationLink(value: task) {
                rowContent
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("tasks.row.\(task.id)")
        }
    }

    @ViewBuilder
    private var mark: some View {
        switch task.group {
        case "running":
            TaskProgressRing(progress: task.progress, reduceMotion: reduceMotion)
                .frame(width: 22, height: 22)
        case "done":
            TaskCompletionMark(reduceMotion: reduceMotion, byYou: task.outcome?.byYou ?? true,
                               filled: task.outcome != nil)
                .frame(width: 24, height: 24)
        case "scheduled":
            Image(systemName: "clock")
                .font(.title3)
                .foregroundStyle(YansuTheme.secondary)
        default:
            EmptyView()
        }
    }

    private var rowContent: some View {
        return HStack(alignment: .center, spacing: 14) {
            if task.group != "needs_you" {
                mark.frame(width: 26)
            }
            if trailing != nil, let onSelect {
                Button(action: onSelect) { rowText }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("tasks.row.open.\(task.id)")
            } else {
                rowText
            }
            if let trailing {
                trailing
            } else {
                Image(systemName: "chevron.right")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .padding(.horizontal, compact ? 12 : 16)
        .padding(.vertical, compact ? 10 : 14)
        .background {
            if !grouped {
                RoundedRectangle(cornerRadius: 18, style: .continuous).fill(YansuTheme.surface)
            }
        }
        .overlay {
            if !grouped {
                RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(YansuTheme.border, lineWidth: 1)
            }
        }
        .contentShape(Rectangle())
    }

    private var rowText: some View {
        let line = TaskThreadCopy.rowLine(task, locale: locale)
        return VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    // The unread dot leads the title at every size, so a wrapped title never pushes it away.
                    if task.unread == true {
                        Circle().fill(YansuTheme.accent).frame(width: 8, height: 8)
                            .accessibilityLabel(String(localized: "New"))
                            .accessibilityIdentifier("tasks.row.unread.\(task.id)")
                    }
                    Text(task.title)
                        .font(compact ? .subheadline.weight(.semibold) : .body.weight(.semibold))
                        .foregroundStyle(YansuTheme.ink)
                        .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : (compact ? 1 : 2))
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !compact {
                    Group {
                        if let lineOverride {
                            Text(lineOverride).foregroundStyle(YansuTheme.accent).fontWeight(.medium)
                        } else if let lead = line.lead {
                            Text(lead).foregroundStyle(YansuTheme.warning).fontWeight(.semibold)
                                + Text(line.rest).foregroundStyle(YansuTheme.secondary)
                        } else {
                            Text(line.rest).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                    .font(.subheadline)
                    .fixedSize(horizontal: false, vertical: true)
                    .contentTransition(.numericText())
                }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct TaskProgressRing: View {
    let progress: MobileTaskProgress?
    let reduceMotion: Bool

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: progress != nil || reduceMotion)) { timeline in
            let rotation = reduceMotion ? 0 : timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 4) * 90
            ZStack {
                Circle()
                    .stroke(YansuTheme.accent.opacity(0.14), lineWidth: 3)
                Circle()
                    .trim(from: 0, to: progress.map { $0.total > 0 ? min(1, max(0, Double($0.done) / Double($0.total))) : 0 } ?? 0.28)
                    .stroke(YansuTheme.accent, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                    .rotationEffect(.degrees(-90 + rotation))
            }
        }
        .animation(.easeInOut(duration: 0.6), value: progress?.done)
        .accessibilityLabel(progress.map {
            String.localizedStringWithFormat(String(localized: "%d of %d"), $0.done, $0.total)
        } ?? String(localized: "In progress"))
    }
}

private struct TaskCompletionMark: View {
    let reduceMotion: Bool
    var byYou = true
    var filled = false
    @State private var drawn = false

    var body: some View {
        Group {
            if filled {
                Image(systemName: "checkmark.circle.fill")
                    .font(.title2)
                    .foregroundStyle(YansuTheme.onSolid, byYou ? YansuTheme.secondary : YansuTheme.solid)
                    .scaleEffect(drawn || reduceMotion ? 1 : 0.6)
            } else if #available(iOS 26.0, *), !reduceMotion {
                Image(systemName: "checkmark.circle")
                    .symbolEffect(.drawOn, isActive: !drawn)
                    .foregroundStyle(YansuTheme.secondary)
            } else {
                ZStack {
                    Circle().stroke(lineWidth: 1.5)
                    Path { path in
                        path.move(to: CGPoint(x: 5, y: 11))
                        path.addLine(to: CGPoint(x: 9, y: 15))
                        path.addLine(to: CGPoint(x: 17, y: 7))
                    }
                    .trim(from: 0, to: drawn || reduceMotion ? 1 : 0)
                    .stroke(style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
                }
                .foregroundStyle(YansuTheme.secondary)
            }
        }
        .onAppear { withAnimation(reduceMotion ? nil : .spring(duration: 0.45)) { drawn = true } }
    }
}
