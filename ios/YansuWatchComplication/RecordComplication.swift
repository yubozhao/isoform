import SwiftUI
import WidgetKit

// The watch face complication: one tap opens Isoform already recording
// (isoformwatch://record, handled in YansuWatchApp). While a recording runs it shows that and
// for how long, read from the app group (WatchLiveState: one start time, nothing else). The
// watch app reloads it when a recording starts or stops, so it never wakes on a schedule. The
// mark is drawn here; the designed one replaces it.
@main
struct IsoformWatchComplications: WidgetBundle {
    var body: some Widget {
        RecordComplication()
    }
}

struct RecordComplication: Widget {
    static let kind = WatchLiveState.complicationKind

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: Self.kind, provider: RecordProvider()) { entry in
            RecordComplicationView(recordingSince: entry.recordingSince)
                .widgetURL(URL(string: "isoformwatch://record"))
                .containerBackground(.clear, for: .widget)
        }
        .configurationDisplayName(Text("Record", comment: "Name of the watch complication that starts a recording"))
        .description(Text("Start a recording in Isoform.", comment: "Description of the watch complication"))
        .supportedFamilies([.accessoryCircular, .accessoryCorner, .accessoryInline, .accessoryRectangular])
    }
}

struct RecordEntry: TimelineEntry {
    let date: Date
    /// When the running recording began; nil when the watch is not recording.
    var recordingSince: Date?
}

struct RecordProvider: TimelineProvider {
    func placeholder(in context: Context) -> RecordEntry { RecordEntry(date: Date()) }

    func getSnapshot(in context: Context, completion: @escaping (RecordEntry) -> Void) {
        completion(RecordEntry(date: Date(), recordingSince: context.isPreview ? nil : WatchLiveState.recordingSince()))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<RecordEntry>) -> Void) {
        let now = Date()
        guard let since = WatchLiveState.recordingSince(now: now) else {
            // Not recording: one entry that never expires. The app reloads it when that changes.
            completion(Timeline(entries: [RecordEntry(date: now)], policy: .never))
            return
        }
        // Recording: the timer counts by itself. One more entry at the longest a recording can
        // run puts the button back if the app was killed and never said it stopped.
        let end = since.addingTimeInterval(WatchLiveState.longest)
        completion(Timeline(entries: [RecordEntry(date: now, recordingSince: since), RecordEntry(date: end)],
                            policy: .never))
    }
}

struct RecordComplicationView: View {
    @Environment(\.widgetFamily) private var family
    var recordingSince: Date?

    var body: some View {
        if let recordingSince {
            recording(since: recordingSince)
        } else {
            idle
        }
    }

    @ViewBuilder
    private func recording(since: Date) -> some View {
        switch family {
        case .accessoryInline:
            Text("Recording \(Text(since, style: .timer))", comment: "Inline watch complication while a recording runs; the value is the elapsed time")
        case .accessoryRectangular:
            HStack(spacing: 8) {
                RecordingMark().frame(width: 30, height: 30)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Recording", comment: "Watch complication while a recording runs").font(.headline)
                    Text(since, style: .timer)
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
        case .accessoryCorner:
            RecordingMark()
                .widgetLabel {
                    Text(since, style: .timer)
                }
        default:
            ZStack {
                AccessoryWidgetBackground()
                RecordingMark().padding(11)
            }
        }
    }

    @ViewBuilder
    private var idle: some View {
        switch family {
        case .accessoryInline:
            Text("Record in Isoform", comment: "Inline watch complication that starts a recording")
        case .accessoryRectangular:
            HStack(spacing: 8) {
                RecordMark().frame(width: 30, height: 30)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Isoform", comment: "Product name; do not translate").font(.headline)
                    Text("Tap to record", comment: "Rectangular watch complication, second line")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
        case .accessoryCorner:
            RecordMark()
                .widgetLabel {
                    Text("Record", comment: "Corner watch complication label")
                }
        default:
            ZStack {
                AccessoryWidgetBackground()
                RecordMark().padding(9)
            }
        }
    }
}

/// A ring around a filled dot: the record mark, drawn so it tints with the watch face.
struct RecordMark: View {
    var body: some View {
        GeometryReader { proxy in
            let side = min(proxy.size.width, proxy.size.height)
            ZStack {
                Circle().strokeBorder(lineWidth: max(1.5, side * 0.09)).opacity(0.55)
                Circle().frame(width: side * 0.5, height: side * 0.5)
            }
            .frame(width: proxy.size.width, height: proxy.size.height)
        }
        .widgetAccentable()
        .accessibilityLabel(Text("Start recording", comment: "Accessibility label of the record complication"))
    }
}

/// A filled rounded square: recording is running. Drawn so it tints with the watch face.
struct RecordingMark: View {
    var body: some View {
        GeometryReader { proxy in
            let side = min(proxy.size.width, proxy.size.height)
            RoundedRectangle(cornerRadius: side * 0.22, style: .continuous)
                .frame(width: side * 0.62, height: side * 0.62)
                .frame(width: proxy.size.width, height: proxy.size.height)
        }
        .widgetAccentable()
        .accessibilityLabel(Text("Recording", comment: "Accessibility label of the complication while a recording runs"))
    }
}
