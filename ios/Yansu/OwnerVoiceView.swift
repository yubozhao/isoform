import SwiftUI

// The phone's "Your voice" page: record a short sample, see the samples the computer holds,
// remove one or all. It is plain on purpose; the designed page replaces it. Settings shows the
// door to it only when the paired computer takes a voice sample from the phone.

enum OwnerVoiceCopy {
    static let intro = String(
        localized: "Record a short sample so Isoform can tell your words from other people's in recordings. It goes to your computer, which keeps a voice signature and no audio. This phone keeps no copy.",
        comment: "Explains the owner's voice sample on the phone")

    static let prompt = String(localized: "Speak in your usual voice for 10 to 30 seconds.",
                               comment: "Shown while the phone records a voice sample")

    static let modelMissing = String(
        localized: "Open Isoform on your computer first. It is still getting what it needs to recognize voices.",
        comment: "The computer has no speaker model yet")

    static func notice(_ notice: OwnerVoiceModel.Notice) -> String {
        switch notice {
        case .added:
            return String(localized: "Added. Isoform now uses it to recognize your voice.",
                          comment: "A voice sample was enrolled from the phone")
        case .removed:
            return String(localized: "Removed from your computer.", comment: "A voice sample was removed")
        case .failed(let error):
            switch error {
            case .tooShort:
                return String(localized: "Isoform needs at least 3 seconds of speech. Record again.",
                              comment: "The voice sample was too short")
            case .refused:
                return String(localized: "Your computer could not use that sample. Record again.",
                              comment: "The voice sample was refused")
            case .modelMissing:
                return modelMissing
            case .notAllowed:
                return String(localized: "This phone is not allowed to add your voice. Pair it again from your computer.",
                              comment: "The pairing lacks the scope for a voice sample")
            case .unreachable:
                return String(localized: "Your computer could not be reached. Try again when it is awake.",
                              comment: "The voice sample could not be sent")
            case .microphoneDenied:
                return String(localized: "Allow the microphone for Isoform in Settings to record a sample.",
                              comment: "The microphone is not allowed")
            }
        }
    }

    static func source(_ sample: VoiceSample) -> String {
        sample.fromPhone == true
            ? String(localized: "From a phone", comment: "A voice sample that was recorded on a phone")
            : String(localized: "From your computer", comment: "A voice sample that was recorded on the computer")
    }

    static func length(_ seconds: Double) -> String {
        String(localized: "\(Int(seconds.rounded())) s", comment: "Length of a voice sample in seconds")
    }
}

struct OwnerVoiceView: View {
    @StateObject private var model: OwnerVoiceModel
    @State private var confirmForgetAll = false

    init(hub: OwnerVoiceHubAPI, session: @escaping @Sendable () throws -> SessionContext) {
        _model = StateObject(wrappedValue: OwnerVoiceModel(
            hub: hub, enroller: OwnerVoiceEnroller(hub: hub, session: session), recorder: OwnerVoiceMicrophone()))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(OwnerVoiceCopy.intro)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                recordCard
                if let notice = model.notice {
                    Text(OwnerVoiceCopy.notice(notice))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("ownerVoice.notice")
                }
                samplesCard
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .yansuScreen()
        .navigationTitle(String(localized: "Your voice", comment: "Title of the phone's page for the owner's voice sample"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.load() }
        .onDisappear { model.leave() }
        .confirmationDialog(String(localized: "Forget your voice?", comment: "Confirms removing every voice sample"),
                            isPresented: $confirmForgetAll, titleVisibility: .visible) {
            Button(String(localized: "Forget my voice", comment: "Removes every voice sample of the owner"), role: .destructive) {
                Task { await model.forget(id: nil) }
            }
        } message: {
            Text("Every sample of your voice is removed from your computer, including those recorded there. Isoform stops telling your words from other people's.",
                 comment: "Explains what forgetting the owner's voice does")
        }
    }

    @ViewBuilder
    private var recordCard: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 10) {
                switch model.phase {
                case .idle:
                    if model.loaded && !model.modelReady {
                        Text(OwnerVoiceCopy.modelMissing).font(.footnote).foregroundStyle(YansuTheme.secondary)
                    }
                    Button {
                        Task { await model.startRecording() }
                    } label: {
                        Text("Record a sample", comment: "Starts recording a voice sample on the phone")
                            .font(.body.weight(.semibold))
                            .foregroundStyle(model.canRecord ? YansuTheme.accent : YansuTheme.tertiary)
                    }
                    .buttonStyle(.plain)
                    .disabled(!model.canRecord)
                    .accessibilityIdentifier("ownerVoice.record")
                case .recording(let since):
                    Text(OwnerVoiceCopy.prompt).font(.footnote).foregroundStyle(YansuTheme.secondary)
                    HStack {
                        Text(since, style: .timer)
                            .font(.body.monospacedDigit())
                            .foregroundStyle(YansuTheme.ink)
                        Spacer()
                        Button {
                            Task { await model.stopAndSend() }
                        } label: {
                            Text("Stop and send", comment: "Stops the voice sample and sends it to the computer")
                                .font(.body.weight(.semibold))
                                .foregroundStyle(YansuTheme.accent)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("ownerVoice.stop")
                    }
                case .sending:
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Sending to your computer", comment: "The voice sample is on its way to the computer")
                            .font(.body)
                            .foregroundStyle(YansuTheme.secondary)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    @ViewBuilder
    private var samplesCard: some View {
        if model.loaded && model.samples.isEmpty {
            Text("No sample yet. Until you add one, recordings do not say which words are yours.",
                 comment: "Empty state of the owner's voice samples on the phone")
                .font(.footnote)
                .foregroundStyle(YansuTheme.tertiary)
                .accessibilityIdentifier("ownerVoice.empty")
        } else if !model.samples.isEmpty {
            YansuCard {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(model.samples) { sample in
                        HStack(alignment: .firstTextBaseline) {
                            VStack(alignment: .leading, spacing: 3) {
                                if let date = sample.date {
                                    Text(date, format: .dateTime.month().day().hour().minute())
                                        .font(.body)
                                        .foregroundStyle(YansuTheme.ink)
                                }
                                Text("\(OwnerVoiceCopy.source(sample)) · \(OwnerVoiceCopy.length(sample.audioSeconds))")
                                    .font(.footnote)
                                    .foregroundStyle(YansuTheme.tertiary)
                            }
                            .accessibilityElement(children: .combine)
                            Spacer()
                            Button {
                                Task { await model.forget(id: sample.id) }
                            } label: {
                                Text("Remove", comment: "Removes one voice sample").font(.footnote)
                                    .foregroundStyle(YansuTheme.secondary)
                            }
                            .buttonStyle(.plain)
                            .disabled(model.phase != .idle)
                        }
                    }
                }
            }
            Button(role: .destructive) {
                confirmForgetAll = true
            } label: {
                Text("Forget my voice", comment: "Removes every voice sample of the owner").font(.body)
            }
            .disabled(model.phase != .idle)
            .accessibilityIdentifier("ownerVoice.forgetAll")
        }
    }
}
