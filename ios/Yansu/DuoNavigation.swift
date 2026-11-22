import Combine
import Foundation

@MainActor
final class DuoNavigation: ObservableObject {
    @Published private(set) var paths: [DuoTab: [DuoStep]] = [
        .today: [.today],
        .watch: [],
        .chat: [.topics]
    ]

    func path(for tab: DuoTab) -> [DuoStep] {
        paths[tab] ?? []
    }

    func push(_ step: DuoStep, on tab: DuoTab) {
        var path = self.path(for: tab)
        path.append(step)
        paths[tab] = path
    }

    func setPushedPath(_ steps: [DuoStep], on tab: DuoTab) {
        let root: DuoStep
        switch tab {
        case .today: root = .today
        // The Watch page pushes its own screens; the wide layout keeps no path for it.
        case .watch:
            paths[tab] = []
            return
        case .chat: root = .topics
        }
        paths[tab] = [root] + steps
    }

    func replaceCurrent(with step: DuoStep, on tab: DuoTab) {
        var path = self.path(for: tab)
        if path.isEmpty {
            path = [step]
        } else {
            path[path.count - 1] = step
        }
        paths[tab] = path
    }

    func close(on tab: DuoTab) {
        var path = self.path(for: tab)
        guard path.count > 1 else { return }
        path.removeLast()
        paths[tab] = path
    }

    func reconcileTodaySelection(isPaired: Bool, repairNeeded: Bool, cardIDs: Set<String>) {
        guard case .item(let id) = path(for: .today).last else { return }
        guard isPaired, !repairNeeded, cardIDs.contains(id) else {
            setPushedPath([], on: .today)
            return
        }
    }

    func visiblePanes(on tab: DuoTab, wide: Bool) -> [DuoStep] {
        let path = self.path(for: tab)
        return Array(path.suffix(wide ? 2 : 1))
    }
}
