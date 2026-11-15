import SwiftUI

// Where the wearer chooses what travels with a recording besides the audio. Each choice is off
// until they turn it on, has its own reason, and asks its own permission then, never at launch.
// Plain on purpose; the designed page replaces it.
struct WatchContextSection: View {
    @AppStorage(WatchContextSettings.heartRateKey) private var heartRate = false

    @AppStorage(WatchContextSettings.locationKey) private var location = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("With recordings", comment: "Heading over the optional context sent with watch recordings")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(WatchTheme.tertiary)
                .frame(maxWidth: .infinity, alignment: .leading)
            if WatchHeartRateSampler.supported {
                NavigationLink {
                    WatchHeartRateChoice()
                } label: {
                    row(Text("Heart rate", comment: "Row on the watch: send heart rate with recordings"), on: heartRate)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("watch.context.heartRate")
            }
            NavigationLink {
                WatchLocationChoice()
            } label: {
                row(Text("Location", comment: "Row on the watch: send coarse location with recordings"), on: location)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("watch.context.location")
        }
        .padding(.top, 4)
    }

    private func row(_ title: Text, on: Bool) -> some View {
        HStack {
            title
                .font(.footnote.weight(.medium))
                .foregroundStyle(WatchTheme.text)
            Spacer()
            Text(on ? WatchContextCopy.on : WatchContextCopy.off)
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
        }
        .padding(.vertical, 7)
        .padding(.horizontal, 9)
        .background(WatchTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

enum WatchContextCopy {
    static let on = String(localized: "On", comment: "An optional context is sent with recordings")
    static let off = String(localized: "Off", comment: "An optional context is not sent with recordings")
    static let heartRateReason = String(
        localized: "While you record, Isoform notes your heart rate so your brief can tell a calm talk from a tense one. It travels sealed with the recording, stays on your devices and is deleted with the recording. Recording works the same without it.",
        comment: "Why the watch asks to read heart rate")
    static let locationReason = String(
        localized: "While you record, Isoform notes roughly where you are, to about a kilometer, so your brief can say where a conversation happened. The area name comes from Apple Maps, which is sent only that rough position. The position travels sealed with the recording, stays on your devices and is deleted with the recording. Recording works the same without it.",
        comment: "Why the watch asks for location")
    static let locationDenied = String(
        localized: "Allow location for Isoform in Settings on this watch.",
        comment: "Shown on the watch when location permission is off")
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

private struct WatchLocationChoice: View {
    @AppStorage(WatchContextSettings.locationKey) private var location = false
    @State private var asking = false
    @State private var denied = false
    @State private var sampler = WatchLocationSampler()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                Text(WatchContextCopy.locationReason)
                    .font(.footnote)
                    .foregroundStyle(WatchTheme.secondary)
                Toggle(isOn: Binding(get: { location }, set: { turnOn in
                    guard turnOn else {
                        location = false
                        WatchPlaceNames.forget()
                        return
                    }
                    asking = true
                    Task {
                        // The system's location sheet appears here, the first time only.
                        let allowed = await sampler.requestAccess()
                        location = allowed
                        denied = !allowed
                        asking = false
                    }
                })) {
                    Text("Location", comment: "Toggle on the watch: send coarse location with recordings")
                }
                .disabled(asking)
                if denied {
                    Text(WatchContextCopy.locationDenied)
                        .font(.footnote)
                        .foregroundStyle(WatchTheme.secondary)
                }
            }
            .padding(.horizontal, 4)
        }
        .background(WatchTheme.ground)
    }
}
