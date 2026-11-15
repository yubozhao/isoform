import SwiftUI

// Where the wearer chooses what travels with a recording besides the audio. Each choice is off
// until they turn it on, has its own reason, and asks its own permission then, never at launch.
// Plain on purpose; the designed page replaces it.
struct WatchContextSection: View {
    @AppStorage(WatchContextSettings.heartRateKey) private var heartRate = false

    var body: some View {
        if WatchHeartRateSampler.supported {
            VStack(alignment: .leading, spacing: 6) {
                Text("With recordings", comment: "Heading over the optional context sent with watch recordings")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(WatchTheme.tertiary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                NavigationLink {
                    WatchHeartRateChoice()
                } label: {
                    HStack {
                        Text("Heart rate", comment: "Row on the watch: send heart rate with recordings")
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(WatchTheme.text)
                        Spacer()
                        Text(heartRate ? WatchContextCopy.on : WatchContextCopy.off)
                            .font(.footnote)
                            .foregroundStyle(WatchTheme.secondary)
                    }
                    .padding(.vertical, 7)
                    .padding(.horizontal, 9)
                    .background(WatchTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("watch.context.heartRate")
            }
            .padding(.top, 4)
        }
    }
}

enum WatchContextCopy {
    static let on = String(localized: "On", comment: "An optional context is sent with recordings")
    static let off = String(localized: "Off", comment: "An optional context is not sent with recordings")
    static let heartRateReason = String(
        localized: "While you record, Isoform notes your heart rate so your brief can tell a calm talk from a tense one. It travels sealed with the recording, stays on your devices and is deleted with the recording. Recording works the same without it.",
        comment: "Why the watch asks to read heart rate")
}

private struct WatchHeartRateChoice: View {
    @AppStorage(WatchContextSettings.heartRateKey) private var heartRate = false
    @State private var asking = false
    private let sampler = WatchHeartRateSampler()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                Text(WatchContextCopy.heartRateReason)
                    .font(.footnote)
                    .foregroundStyle(WatchTheme.secondary)
                Toggle(isOn: Binding(get: { heartRate }, set: { turnOn in
                    guard turnOn else {
                        heartRate = false
                        return
                    }
                    asking = true
                    Task {
                        // The system's Health sheet appears here, the first time only.
                        heartRate = await sampler.requestAccess()
                        asking = false
                    }
                })) {
                    Text("Heart rate", comment: "Toggle on the watch: send heart rate with recordings")
                }
                .disabled(asking)
            }
            .padding(.horizontal, 4)
        }
        .background(WatchTheme.ground)
    }
}
