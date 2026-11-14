import SwiftUI
import WidgetKit

// The watch face complication: one tap opens Isoform already recording
// (isoformwatch://record, handled in YansuWatchApp). It shows no live state, so it needs no
// shared container and never wakes to refresh. The mark is drawn here; the designed one
// replaces it.
@main
struct IsoformWatchComplications: WidgetBundle {
    var body: some Widget {
        RecordComplication()
    }
}

struct RecordComplication: Widget {
    static let kind = "com.isoform.companion.watch.record"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: Self.kind, provider: RecordProvider()) { _ in
            RecordComplicationView()
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
}

struct RecordProvider: TimelineProvider {
    func placeholder(in context: Context) -> RecordEntry { RecordEntry(date: Date()) }

    func getSnapshot(in context: Context, completion: @escaping (RecordEntry) -> Void) {
        completion(RecordEntry(date: Date()))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<RecordEntry>) -> Void) {
        // One entry that never expires: the complication is a button, not a readout.
        completion(Timeline(entries: [RecordEntry(date: Date())], policy: .never))
    }
}

struct RecordComplicationView: View {
    @Environment(\.widgetFamily) private var family

    var body: some View {
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
