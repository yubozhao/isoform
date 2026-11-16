import Foundation

// What the watch's record control does with a touch. Starting is a tap; while recording a tap
// marks the moment. Stopping is a hold of
// `holdSeconds`, so a bump of the wrist or a sleeve never ends a recording. A hold let go early
// changes nothing. VoiceOver's activate is deliberate, so it does both.
enum WatchRecordControl {
    /// How long the control is held before a recording stops.
    static let holdSeconds: Double = 1

    enum Touch: Equatable {
        case tap
        /// The control was held for `seconds` and is still down or was just let go.
        case hold(seconds: Double)
        /// VoiceOver's activate (a double tap with VoiceOver on).
        case accessibilityActivate
        /// The system double tap (thumb and finger, watchOS 11 on watches that have it).
        case doubleTap
        /// The Action button, through the "Mark a moment" shortcut.
        case actionButton
    }

    enum Action: Equatable {
        /// `mark`: the wearer says "this matters" about the moment (PROTOCOL.md 24.9).
        case none, start, stop, mark
    }

    static func action(for touch: Touch, isRecording: Bool) -> Action {
        switch touch {
        case .tap:
            // A tap never stops a recording. While recording it marks the moment.
            return isRecording ? .mark : .start
        case .hold(let seconds):
            guard seconds >= holdSeconds else { return .none }
            return isRecording ? .stop : .start
        case .accessibilityActivate:
            return isRecording ? .stop : .start
        case .doubleTap, .actionButton:
            // Like a tap: they mark while recording and start otherwise. They never stop
            // (holding the Action button is the system's Siren, so nothing is asked of a hold).
            return isRecording ? .mark : .start
        }
    }

    /// How far the hold has come, from 0 to 1, for the ring drawn round the control.
    static func progress(heldSeconds: Double) -> Double {
        guard heldSeconds.isFinite, heldSeconds > 0 else { return 0 }
        return min(1, heldSeconds / holdSeconds)
    }
}
