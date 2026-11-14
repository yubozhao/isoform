import Foundation

enum WatchRecordingCopy {
    /// The one line under a recording that says where it is.
    static func state(_ display: WatchRecordingDisplay) -> String {
        switch display {
        case .recording:
            return String(localized: "Recording", comment: "State of a recording on the watch: still recording")
        case .onWatch:
            return String(localized: "On this watch", comment: "State of a recording: not yet sent to the phone")
        case .sending(let sent, let total):
            return String(localized: "Sending to your phone, \(sent) of \(total)",
                          comment: "State of a recording: parts sent to the phone so far")
        case .remote(.onPhone):
            return String(localized: "On your phone", comment: "State of a recording: the phone has it, the computer does not yet")
        case .remote(.onComputer):
            return String(localized: "On your computer", comment: "State of a recording: the computer has it")
        case .remote(.transcribed):
            return String(localized: "In Isoform", comment: "State of a recording: transcribed on the computer and part of memory")
        case .remote(.failed):
            return String(localized: "Could not be read", comment: "State of a recording: the computer could not transcribe it")
        }
    }
}
