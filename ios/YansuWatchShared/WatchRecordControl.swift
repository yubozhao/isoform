import Foundation

// What the watch's record control does with a touch. Starting is a tap. Stopping is a hold of
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
    }

    enum Action: Equatable {
        case none, start, stop
    }

    static func action(for touch: Touch, isRecording: Bool) -> Action {
        switch touch {
        case .tap:
            return isRecording ? .none : .start
        case .hold(let seconds):
            guard seconds >= holdSeconds else { return .none }
            return isRecording ? .stop : .start
        case .accessibilityActivate:
            return isRecording ? .stop : .start
        }
    }

    /// How far the hold has come, from 0 to 1, for the ring drawn round the control.
    static func progress(heldSeconds: Double) -> Double {
        guard heldSeconds.isFinite, heldSeconds > 0 else { return 0 }
        return min(1, heldSeconds / holdSeconds)
    }
}
