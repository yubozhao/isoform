import SwiftUI

// The Watch item in the tab bar and the wide rail: Isoform's mark in the watch's pose
// (phone-watch v1.1.0 rule 3). It makes the watch's state visible on every screen.
//
// - Listening: the lifted brow, in the accent.
// - Closed Eyes: the cap, in amber.
// - Not listening: the shut eye, in grey.
//
// A small amber dot sits on it whenever something needs looking at: Not listening, an hour that
// could not be sent, or hours that cannot go because the computer has not been reached for 30
// minutes or more. Hours that are going leave it plain (phone-watch v1.2.0 rule 7).

enum WatchPose: Equatable, Sendable {
    case listening
    case closedEyes
    case shut

    /// The pose for what the Watch page's Now card says.
    static func of(_ kind: WatchNow.Kind) -> WatchPose {
        switch kind {
        // Out of reach of the phone the watch keeps listening.
        case .listening, .notNear: return .listening
        case .closedEyes: return .closedEyes
        case .offToday, .notListening, .setUp, .unknown: return .shut
        }
    }
}

/// What the Watch item shows: the pose, and whether the amber dot is on.
struct WatchItemState: Equatable, Sendable {
    let pose: WatchPose
    let needsLook: Bool

    static func of(connection: WatchConnection, status: WatchLiveStatus?, rows: [WatchRecordingRow],
                   outcome: WatchRecordingForwarder.Outcome = .drained, now: Date = Date()) -> WatchItemState {
        let kind = WatchTabCopy.now(connection: connection, status: status, pauseUntil: nil, lastHeard: nil).kind
        return WatchItemState(pose: .of(kind), needsLook: kind == .notListening || cannotGo(rows, outcome: outcome, now: now))
    }

    /// An hour could not be sent, or hours on this phone have waited 30 minutes while the last
    /// try to send did not get through. Hours on the watch, and hours that are going, are plain.
    static func cannotGo(_ rows: [WatchRecordingRow], outcome: WatchRecordingForwarder.Outcome, now: Date = Date()) -> Bool {
        if rows.contains(where: { $0.stage == .failedToSend || $0.stage == .failedOnComputer }) { return true }
        guard outcome != .drained, outcome != .sending else { return false }
        return rows.contains { row in
            (row.stage == .onPhone || row.stage == .sending)
                && now.timeIntervalSince(row.startedAt.addingTimeInterval(row.durationSeconds)) >= 30 * 60
        }
    }
}

struct WatchPoseMark: View {
    let state: WatchItemState
    /// The item is the selected tab: the listening pose keeps the accent, the others their own color.
    var selected = false
    var size: CGFloat = 20

    private var ink: Color {
        switch state.pose {
        case .listening: return YansuTheme.accent
        case .closedEyes: return YansuTheme.warning
        case .shut: return selected ? YansuTheme.secondary : YansuTheme.tertiary
        }
    }

    var body: some View {
        ZStack(alignment: .topTrailing) {
            ZStack {
                // The small circle of the mark, the same in every pose.
                Circle()
                    .fill(ink.opacity(0.45))
                    .frame(width: size * 0.34, height: size * 0.34)
                    .offset(x: size * 0.24, y: size * 0.2)
                eye.offset(x: -size * 0.1, y: -size * 0.04)
            }
            .frame(width: size, height: size)
            if state.needsLook {
                Circle()
                    .fill(YansuTheme.warning)
                    .frame(width: size * 0.3, height: size * 0.3)
                    .offset(x: size * 0.16, y: -size * 0.12)
                    .accessibilityIdentifier("watch.item.dot")
            }
        }
        .accessibilityHidden(true)
    }

    @ViewBuilder private var eye: some View {
        let eyeSize = size * 0.5
        switch state.pose {
        case .listening:
            // The open eye with its brow lifted over it.
            ZStack {
                Circle().fill(ink).frame(width: eyeSize, height: eyeSize)
                Capsule().fill(ink)
                    .frame(width: eyeSize * 0.9, height: size * 0.09)
                    .rotationEffect(.degrees(-14))
                    .offset(y: -eyeSize * 0.82)
            }
        case .closedEyes:
            // The cap pulled over the eye.
            ZStack {
                Circle().fill(ink.opacity(0.35)).frame(width: eyeSize, height: eyeSize)
                Circle().fill(ink).frame(width: eyeSize, height: eyeSize)
                    .mask(alignment: .top) { Rectangle().frame(height: eyeSize * 0.55) }
                Capsule().fill(ink)
                    .frame(width: eyeSize * 1.25, height: size * 0.09)
                    .offset(x: eyeSize * 0.12, y: eyeSize * 0.05)
            }
        case .shut:
            // The eye shut to a line.
            Capsule().fill(ink).frame(width: eyeSize, height: size * 0.11)
        }
    }
}
