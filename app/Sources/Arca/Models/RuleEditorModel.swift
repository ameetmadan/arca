import Foundation
import Combine

/// State of the rule editor sheet: the form fields, debounced live validation
/// through `POST /rules/validate`, saving, and "Test against a file".
@MainActor
final class RuleEditorModel: ObservableObject {
    enum Mode: Equatable {
        case create
        case edit(originalID: String)
    }

    enum ValidationState: Equatable {
        /// Nothing to check yet (empty expression).
        case idle
        case checking
        case valid
        case invalid
        /// The validate call itself failed (backend down, rate limit, ...).
        case unavailable(String)
    }

    enum TestState: Equatable {
        case idle
        case running
        case result(RuleTestResult)
        case failed(String)
    }

    /// Delay between the last keystroke and the validate request.
    static let debounce: Duration = .milliseconds(400)

    let mode: Mode

    @Published var id: String { didSet { if id != oldValue { fieldsChanged() } } }
    @Published var when: String { didSet { if when != oldValue { fieldsChanged() } } }
    @Published var action: RuleAction { didSet { if action != oldValue { fieldsChanged() } } }
    @Published var target: String { didSet { if target != oldValue { fieldsChanged() } } }
    @Published var reason: String { didSet { if reason != oldValue { fieldsChanged() } } }

    @Published private(set) var validation: ValidationState = .idle
    /// Problems reported by `/rules/validate` for the current text.
    @Published private(set) var validationIssues: [RuleIssue] = []
    /// Problems reported by the last failed save (409/422).
    @Published private(set) var saveIssues: [RuleIssue] = []
    @Published private(set) var saveError: String?
    @Published private(set) var isSaving = false

    @Published private(set) var testState: TestState = .idle
    @Published private(set) var testedFileName: String?

    private var validationTask: Task<Void, Never>?

    init(editing rule: Rule?) {
        if let rule = rule {
            mode = .edit(originalID: rule.id)
            id = rule.id
            when = rule.when
            action = RuleAction(rawValue: rule.action) ?? .ignore
            target = rule.target
            reason = rule.reason
        } else {
            mode = .create
            id = ""
            when = ""
            action = .move
            target = ""
            reason = ""
        }
    }

    var isEditing: Bool {
        if case .edit = mode { return true }
        return false
    }

    var title: String { isEditing ? "Edit rule" : "New rule" }

    /// The id the server will know this rule by, used to pick out the issues
    /// that belong to it.
    private var currentID: String {
        if case .edit(let original) = mode { return original }
        return id.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var trimmedWhen: String { when.trimmingCharacters(in: .whitespacesAndNewlines) }

    // MARK: Drafts

    /// Only the fields that mean something for the chosen action are sent, so
    /// switching from "move" to "ignore" never leaves a stale target behind.
    private func draft(includeID: Bool, forSave: Bool) -> RuleDraft {
        var draft = RuleDraft()
        let trimmedID = id.trimmingCharacters(in: .whitespacesAndNewlines)
        if includeID, !trimmedID.isEmpty { draft.id = trimmedID }
        draft.when = when
        draft.action = action.rawValue
        let trimmedTarget = target.trimmingCharacters(in: .whitespacesAndNewlines)
        if action.usesTarget {
            if forSave || !trimmedTarget.isEmpty { draft.target = trimmedTarget }
        } else if forSave {
            draft.target = ""
        }
        if forSave {
            draft.reason = reason
        } else if !reason.isEmpty {
            draft.reason = reason
        }
        return draft
    }

    // MARK: Validation

    /// Called after any field edit: forget old server answers and re-validate
    /// after a short pause.
    private func fieldsChanged() {
        saveIssues = []
        saveError = nil
        scheduleValidation()
    }

    func scheduleValidation() {
        validationTask?.cancel()
        guard !trimmedWhen.isEmpty else {
            validation = .idle
            validationIssues = []
            return
        }
        validation = .checking
        let candidate = draft(includeID: !isEditing, forSave: false)
        validationTask = Task { [weak self] in
            try? await Task.sleep(for: RuleEditorModel.debounce)
            guard !Task.isCancelled else { return }
            do {
                let result = try await APIClient.shared.validateRule(candidate)
                guard !Task.isCancelled, let self = self else { return }
                self.apply(result)
            } catch {
                guard !Task.isCancelled, let self = self else { return }
                self.validationIssues = []
                self.validation = .unavailable(error.localizedDescription)
            }
        }
    }

    private func apply(_ result: RuleValidation) {
        validationIssues = result.issues
        if result.valid && result.issues.isEmpty {
            validation = .valid
        } else {
            validation = .invalid
        }
    }

    // MARK: Issues, by field

    private var allIssues: [RuleIssue] { validationIssues + saveIssues }

    private func belongsToThisRule(_ issue: RuleIssue) -> Bool {
        guard let owner = issue.ruleID, !owner.isEmpty else { return true }
        return owner == currentID
    }

    /// Issues for one form field (`id`, `when`, `action`, `target`, `reason`).
    func issues(for field: String) -> [RuleIssue] {
        allIssues.filter { $0.field == field && belongsToThisRule($0) }
    }

    private static let formFields: Set<String> = ["id", "when", "action", "target", "reason"]

    /// Everything that is not next to a field: whole-rule problems, the rule
    /// count limit, and problems the server found in a *different* stored rule.
    var generalIssues: [RuleIssue] {
        allIssues.filter { !(Self.formFields.contains($0.field) && belongsToThisRule($0)) }
    }

    func describe(_ issue: RuleIssue) -> String {
        if let owner = issue.ruleID, !owner.isEmpty, owner != currentID {
            return "Another rule (\(owner)): \(issue.message)"
        }
        return issue.message
    }

    // MARK: Saving

    var canSave: Bool {
        guard !isSaving, validation == .valid, saveIssues.isEmpty, saveError == nil else { return false }
        if !isEditing, id.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return false }
        return true
    }

    /// Returns the saved rule, or nil after putting the server's complaint into
    /// `saveIssues` / `saveError`.
    func save() async -> Rule? {
        guard canSave else { return nil }
        isSaving = true
        defer { isSaving = false }
        do {
            let saved: RuleSaved
            switch mode {
            case .create:
                saved = try await APIClient.shared.createRule(draft(includeID: true, forSave: true))
            case .edit(let originalID):
                saved = try await APIClient.shared.updateRule(id: originalID, draft: draft(includeID: false, forSave: true))
            }
            return saved.rule
        } catch RulesAPIError.issues(let message, let issues, _) {
            saveIssues = issues
            if issues.isEmpty { saveError = message }
            return nil
        } catch {
            saveError = error.localizedDescription
            return nil
        }
    }

    // MARK: Test against a file

    var canTest: Bool {
        if case .running = testState { return false }
        return !trimmedWhen.isEmpty
    }

    func runTest(file: URL, watchedFolders: [URL]) async {
        testState = .running
        testedFileName = file.lastPathComponent
        do {
            let facts = try RuleFactsBuilder.build(for: file, roots: watchedFolders)
            let result = try await APIClient.shared.testRule(draft(includeID: false, forSave: false), facts: facts)
            testState = .result(result)
        } catch RulesAPIError.issues(let message, let issues, _) {
            let joined = issues.map { $0.message }.joined(separator: " ")
            testState = .failed(joined.isEmpty ? message : joined)
        } catch {
            testState = .failed(error.localizedDescription)
        }
    }
}
