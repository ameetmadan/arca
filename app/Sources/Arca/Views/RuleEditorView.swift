import SwiftUI
import AppKit

/// A status line whose meaning is carried by an icon and by the words, not by
/// colour alone. The text stays in the primary colour for contrast.
struct StatusLabel: View {
    let text: String
    let systemImage: String
    let tint: Color

    var body: some View {
        Label {
            Text(text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: systemImage).foregroundStyle(tint)
        }
        .accessibilityElement(children: .combine)
    }
}

/// Modal editor for one rule: create or edit. Validation runs against the
/// server while the user types; the server's messages are shown next to the
/// field they belong to, and Save stays disabled until the rule is valid.
struct RuleEditorView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: RuleEditorModel
    @FocusState private var focus: Field?

    private let limits: RuleLimits
    private let watchedFolders: [URL]
    private let onSaved: (Rule) -> Void

    private enum Field: Hashable {
        case id, when, target, reason
    }

    init(editing rule: Rule?, limits: RuleLimits, watchedFolders: [URL], onSaved: @escaping (Rule) -> Void) {
        _model = StateObject(wrappedValue: RuleEditorModel(editing: rule))
        self.limits = limits
        self.watchedFolders = watchedFolders
        self.onSaved = onSaved
    }

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text(model.title).font(.title2.weight(.semibold))
                        .accessibilityAddTraits(.isHeader)

                    idField
                    expressionField
                    actionField
                    targetField
                    reasonField
                    generalIssues

                    DisclosureGroup("Expression help") {
                        RulesHelpView().padding(.top, 6)
                    }

                    Divider()
                    testSection
                }
                .padding(20)
            }

            Divider()
            footer
        }
        .frame(width: 580, height: 680)
        .task {
            model.scheduleValidation()
            focus = model.isEditing ? .when : .id
        }
    }

    // MARK: Fields

    private var idField: some View {
        FieldBlock(title: "Name") {
            TextField("Name", text: $model.id)
                .labelsHidden()
                .textFieldStyle(.roundedBorder)
                .focused($focus, equals: .id)
                .disabled(model.isEditing)
                .autocorrectionDisabled()
                .accessibilityHint(model.isEditing
                    ? "The name of an existing rule cannot be changed."
                    : "Letters, digits, dot, underscore and hyphen. At most \(limits.maxIDLength) characters.")
            Text(model.isEditing
                 ? "The name cannot be changed. Delete the rule and create a new one to rename it."
                 : "Letters, digits, . _ and -. Shown in the list and used as the rule's identifier.")
                .font(.caption).foregroundStyle(.secondary)
            IssueList(model: model, field: "id")
        }
    }

    private var expressionField: some View {
        FieldBlock(title: "Expression") {
            TextField("Expression", text: $model.when, axis: .vertical)
                .labelsHidden()
                .textFieldStyle(.roundedBorder)
                .font(.system(.body, design: .monospaced))
                .lineLimit(3...8)
                .focused($focus, equals: .when)
                .autocorrectionDisabled()
                .accessibilityHint("A condition that is true or false. File extensions include the leading dot, for example .pdf.")
            HStack(alignment: .firstTextBaseline) {
                expressionStatus
                Spacer()
                Text("\(model.when.utf8.count) / \(limits.maxExpressionLength)")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("\(model.when.utf8.count) of \(limits.maxExpressionLength) bytes used")
            }
            IssueList(model: model, field: "when")
        }
    }

    @ViewBuilder private var expressionStatus: some View {
        switch model.validation {
        case .idle:
            Text("Type an expression to check it.").font(.caption).foregroundStyle(.secondary)
        case .checking:
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Checking…").font(.caption).foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .combine)
        case .valid:
            StatusLabel(text: "Expression is valid", systemImage: "checkmark.circle.fill", tint: .green)
                .font(.caption)
        case .invalid:
            StatusLabel(text: "This rule has errors", systemImage: "xmark.octagon.fill", tint: .red)
                .font(.caption)
        case .unavailable(let message):
            StatusLabel(text: "Could not check the expression: \(message)", systemImage: "exclamationmark.triangle.fill", tint: .orange)
                .font(.caption)
        }
    }

    private var actionField: some View {
        FieldBlock(title: "Action") {
            Picker("Action", selection: $model.action) {
                ForEach(RuleAction.allCases) { action in
                    Text(action.title).tag(action)
                }
            }
            .labelsHidden()
            .pickerStyle(.menu)
            .fixedSize()
            IssueList(model: model, field: "action")
        }
    }

    private var targetField: some View {
        FieldBlock(title: model.action.targetLabel) {
            TextField(model.action.targetLabel, text: $model.target, prompt: Text(model.action.targetPlaceholder))
                .labelsHidden()
                .textFieldStyle(.roundedBorder)
                .focused($focus, equals: .target)
                .disabled(!model.action.usesTarget)
                .autocorrectionDisabled()
            if !model.action.usesTarget {
                Text("This action does not use a target.").font(.caption).foregroundStyle(.secondary)
            }
            IssueList(model: model, field: "target")
        }
    }

    private var reasonField: some View {
        FieldBlock(title: "Reason (optional)") {
            TextField("Reason", text: $model.reason, prompt: Text("Shown to you when this rule decides"))
                .labelsHidden()
                .textFieldStyle(.roundedBorder)
                .focused($focus, equals: .reason)
            HStack {
                Text("Do not put file contents in the reason.").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Text("\(model.reason.count) / \(limits.maxReasonLength)")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("\(model.reason.count) of \(limits.maxReasonLength) characters used")
            }
            IssueList(model: model, field: "reason")
        }
    }

    @ViewBuilder private var generalIssues: some View {
        let issues = model.generalIssues
        if !issues.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(issues) { issue in
                    StatusLabel(text: model.describe(issue), systemImage: "exclamationmark.triangle.fill", tint: .red)
                        .font(.callout)
                }
            }
        }
        if let message = model.saveError {
            StatusLabel(text: message, systemImage: "exclamationmark.triangle.fill", tint: .red)
                .font(.callout)
        }
    }

    // MARK: Test

    private var testSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Test against a file").font(.headline)
                .accessibilityAddTraits(.isHeader)
            Text("Pick a file to see whether this rule would match it. Nothing is saved and no file is changed. The file's path, type, size and date, and the first 8 KB of a plain-text file, are sent to the Arca backend on this Mac.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            HStack {
                Button("Choose File…") { chooseFile() }
                    .disabled(!model.canTest)
                    .accessibilityHint("Opens a file picker and tests the rule against the file you choose.")
                if let name = model.testedFileName {
                    Text(name).lineLimit(1).truncationMode(.middle).foregroundStyle(.secondary)
                }
            }
            testResult
        }
    }

    @ViewBuilder private var testResult: some View {
        let file = model.testedFileName ?? "the file"
        switch model.testState {
        case .idle:
            EmptyView()
        case .running:
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Testing…")
            }
            .accessibilityElement(children: .combine)
        case .failed(let message):
            StatusLabel(text: message, systemImage: "exclamationmark.triangle.fill", tint: .red)
        case .result(let result):
            VStack(alignment: .leading, spacing: 4) {
                if let problem = result.evaluationError {
                    StatusLabel(text: "The rule could not be evaluated on \(file): \(problem)", systemImage: "exclamationmark.triangle.fill", tint: .orange)
                } else if result.matched {
                    StatusLabel(text: "Matched: this rule would apply to \(file).", systemImage: "checkmark.circle.fill", tint: .green)
                    if let decision = result.decision {
                        Text(decisionSummary(decision)).font(.callout).foregroundStyle(.secondary)
                    }
                } else {
                    StatusLabel(text: "Not matched: this rule would not apply to \(file).", systemImage: "minus.circle.fill", tint: .secondary)
                }
                if let reason = result.reason, !reason.isEmpty, !result.matched {
                    Text(reason).font(.caption).foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
        }
    }

    private func decisionSummary(_ decision: RuleDecision) -> String {
        var text = RuleAction.title(forRaw: decision.action)
        if let target = decision.target, !target.isEmpty { text += ": \(target)" }
        return text
    }

    private func chooseFile() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.message = "Choose a file to test this rule against"
        panel.prompt = "Test"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { await model.runTest(file: url, watchedFolders: watchedFolders) }
    }

    // MARK: Footer

    private var footer: some View {
        VStack(alignment: .trailing, spacing: 6) {
            if !model.canSave && !model.isSaving {
                Text(saveHint).font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Save") { save() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!model.canSave)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }

    private var saveHint: String {
        if !model.isEditing && model.id.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Save is available once the rule has a name and a valid expression."
        }
        switch model.validation {
        case .valid: return "Change something to try saving again."
        case .checking: return "Waiting for the expression check."
        default: return "Save is available once the rule is valid."
        }
    }

    private func save() {
        Task {
            if let saved = await model.save() {
                onSaved(saved)
                dismiss()
            }
        }
    }
}

/// A titled block in the editor. The visible title is hidden from VoiceOver
/// because each control carries its own accessibility label.
private struct FieldBlock<Content: View>: View {
    let title: String
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.headline).accessibilityHidden(true)
            content
        }
    }
}

/// The server's messages for one field, each with an icon and the word "Error".
private struct IssueList: View {
    @ObservedObject var model: RuleEditorModel
    let field: String

    var body: some View {
        let issues = model.issues(for: field)
        if !issues.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                ForEach(issues) { issue in
                    StatusLabel(text: issue.message, systemImage: "exclamationmark.triangle.fill", tint: .red)
                        .font(.callout)
                        .accessibilityLabel("Error: \(issue.message)")
                }
            }
        }
    }
}
