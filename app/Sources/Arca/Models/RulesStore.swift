import Foundation
import Combine
import SwiftUI

/// State for the rules list in Settings. All HTTP goes through APIClient.
@MainActor
final class RulesStore: ObservableObject {
    @Published private(set) var rules: [Rule] = []
    @Published private(set) var limits: RuleLimits = .fallback
    @Published private(set) var suggestions: [RuleSuggestion] = []
    @Published private(set) var isLoading = false
    /// True while a reorder or delete request is in flight.
    @Published private(set) var isBusy = false
    @Published var errorMessage: String?

    func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let list = try await APIClient.shared.listRules()
            rules = list.rules
            if let limits = list.limits { self.limits = limits }
            errorMessage = nil
        } catch {
            errorMessage = Self.describe(error)
        }
        // Suggestions are optional: a failure here must not hide the rules.
        if let result = try? await APIClient.shared.ruleSuggestions() {
            suggestions = result.suggestions
        } else {
            suggestions = []
        }
    }

    /// Drag-and-drop reorder from the List. The new order is shown at once and
    /// rolled back (then refetched) if the server refuses it.
    func move(fromOffsets: IndexSet, toOffset: Int) async {
        guard !isBusy else { return }
        let previous = rules
        var reordered = rules
        reordered.move(fromOffsets: fromOffsets, toOffset: toOffset)
        guard reordered.map({ $0.id }) != previous.map({ $0.id }) else { return }
        rules = reordered
        await commitOrder(previous: previous)
    }

    /// Keyboard / VoiceOver alternative to dragging: move one row up (-1) or down (+1).
    func move(id: String, by delta: Int) async {
        guard let from = rules.firstIndex(where: { $0.id == id }) else { return }
        let to = from + delta
        guard rules.indices.contains(to) else { return }
        // `toOffset` is the insertion index before removal, so moving down needs +1.
        await move(fromOffsets: IndexSet(integer: from), toOffset: delta > 0 ? to + 1 : to)
    }

    func delete(id: String) async {
        guard !isBusy else { return }
        isBusy = true
        defer { isBusy = false }
        do {
            _ = try await APIClient.shared.deleteRule(id: id)
            rules.removeAll { $0.id == id }
            errorMessage = nil
        } catch {
            let message = Self.describe(error)
            await load()
            errorMessage = message
        }
    }

    private func commitOrder(previous: [Rule]) async {
        isBusy = true
        defer { isBusy = false }
        do {
            let result = try await APIClient.shared.setRuleOrder(rules.map { $0.id })
            rules = result.rules
            errorMessage = nil
        } catch {
            rules = previous
            // A 422 usually means the list changed under us; refetch, but keep the message.
            let message = Self.describe(error)
            await load()
            errorMessage = message
        }
    }

    static func describe(_ error: Error) -> String {
        error.localizedDescription
    }
}
