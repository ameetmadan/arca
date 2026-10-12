import SwiftUI

/// What the editor sheet is opened for.
enum RuleEditorTarget: Identifiable {
    case create
    case edit(Rule)

    var id: String {
        switch self {
        case .create: return "new-rule"
        case .edit(let rule): return "edit-" + rule.id
        }
    }

    var rule: Rule? {
        if case .edit(let rule) = self { return rule }
        return nil
    }
}

/// The "Rules" tab of Settings: an ordered list (first match wins) with
/// reorder, edit and delete, plus the suggested-rules section when there is one.
///
/// The rules API has no enabled/disabled flag, so there is deliberately no
/// toggle here: to switch a rule off, delete it.
struct RulesView: View {
    @EnvironmentObject private var appState: AppState
    @StateObject private var store = RulesStore()
    @State private var selection: String?
    @State private var editorTarget: RuleEditorTarget?
    @State private var pendingDelete: Rule?

    private var selectedRule: Rule? {
        guard let selection = selection else { return nil }
        return store.rules.first { $0.id == selection }
    }

    private var selectedIndex: Int? {
        guard let selection = selection else { return nil }
        return store.rules.firstIndex { $0.id == selection }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Rules run before any AI is asked. They are tried from top to bottom and the first match decides what happens to a file. Drag a rule to reorder it, or use Move Up and Move Down.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if let message = store.errorMessage {
                StatusLabel(text: message, systemImage: "exclamationmark.triangle.fill", tint: .red)
                    .font(.callout)
            }

            rulesList
            buttonBar

            if !store.suggestions.isEmpty {
                suggestionsSection
            }
        }
        .padding(20)
        .frame(width: 620, height: 520)
        .task { await store.load() }
        .sheet(item: $editorTarget) { target in
            RuleEditorView(
                editing: target.rule,
                limits: store.limits,
                watchedFolders: appState.watchedFolders
            ) { saved in
                selection = saved.id
                Task { await store.load() }
            }
        }
        .confirmationDialog(
            "Delete this rule?",
            isPresented: Binding(
                get: { pendingDelete != nil },
                set: { if !$0 { pendingDelete = nil } }
            ),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { rule in
            Button("Delete “\(rule.id)”", role: .destructive) {
                selection = nil
                Task { await store.delete(id: rule.id) }
            }
        } message: { rule in
            Text("The rule “\(rule.id)” will be removed. This cannot be undone.")
        }
    }

    // MARK: List

    private var rulesList: some View {
        List(selection: $selection) {
            ForEach(Array(store.rules.enumerated()), id: \.element.id) { index, rule in
                RuleRow(rule: rule, position: index + 1, total: store.rules.count)
                    .contextMenu {
                        Button("Edit…") { editorTarget = .edit(rule) }
                        Button("Move Up") { Task { await store.move(id: rule.id, by: -1) } }
                            .disabled(index == 0)
                        Button("Move Down") { Task { await store.move(id: rule.id, by: 1) } }
                            .disabled(index == store.rules.count - 1)
                        Divider()
                        Button("Delete…", role: .destructive) { pendingDelete = rule }
                    }
                    .accessibilityAction(named: "Edit") { editorTarget = .edit(rule) }
                    .accessibilityAction(named: "Move up") { Task { await store.move(id: rule.id, by: -1) } }
                    .accessibilityAction(named: "Move down") { Task { await store.move(id: rule.id, by: 1) } }
                    .accessibilityAction(named: "Delete") { pendingDelete = rule }
            }
            .onMove { source, destination in
                Task { await store.move(fromOffsets: source, toOffset: destination) }
            }
        }
        .onDeleteCommand {
            if let rule = selectedRule { pendingDelete = rule }
        }
        .overlay {
            if store.rules.isEmpty && !store.isLoading {
                ContentUnavailableView(
                    "No rules yet",
                    systemImage: "list.bullet.rectangle",
                    description: Text("Choose New Rule to add one.")
                )
            }
        }
        .accessibilityLabel("Rules, in the order they are tried")
    }

    private var buttonBar: some View {
        HStack {
            Button {
                editorTarget = .create
            } label: {
                Label("New Rule", systemImage: "plus")
            }

            Button("Edit…") {
                if let rule = selectedRule { editorTarget = .edit(rule) }
            }
            .disabled(selectedRule == nil)

            Button("Delete…", role: .destructive) {
                pendingDelete = selectedRule
            }
            .disabled(selectedRule == nil || store.isBusy)

            Spacer()

            Button {
                if let rule = selectedRule { Task { await store.move(id: rule.id, by: -1) } }
            } label: {
                Label("Move Up", systemImage: "arrow.up")
            }
            .disabled(selectedIndex == nil || selectedIndex == 0 || store.isBusy)
            .help("Move the selected rule up. It is tried earlier.")

            Button {
                if let rule = selectedRule { Task { await store.move(id: rule.id, by: 1) } }
            } label: {
                Label("Move Down", systemImage: "arrow.down")
            }
            .disabled(selectedIndex == nil || selectedIndex == store.rules.count - 1 || store.isBusy)
            .help("Move the selected rule down. It is tried later.")
        }
    }

    // MARK: Suggestions

    /// Hidden while the server has no suggestions (it never has any yet). Their
    /// shape is not defined by the API, so there is nothing to accept or
    /// dismiss yet; this only says that some exist.
    private var suggestionsSection: some View {
        GroupBox("Suggested rules") {
            Label(
                store.suggestions.count == 1
                    ? "1 rule is suggested. Reviewing suggestions is not available in this version."
                    : "\(store.suggestions.count) rules are suggested. Reviewing suggestions is not available in this version.",
                systemImage: "lightbulb"
            )
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(4)
        }
    }
}

private struct RuleRow: View {
    let rule: Rule
    let position: Int
    let total: Int

    private var actionSummary: String {
        var text = RuleAction.title(forRaw: rule.action)
        if !rule.target.isEmpty { text += ": \(rule.target)" }
        return text
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text("\(position)")
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
                .frame(width: 28, alignment: .trailing)

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    Text(rule.id).fontWeight(.semibold)
                    Text(actionSummary).foregroundStyle(.secondary)
                    if !rule.valid {
                        Label("Invalid", systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(.red)
                    }
                }
                Text(rule.when)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
                if !rule.valid, let issue = rule.issue {
                    Text(issue.message)
                        .font(.caption)
                        .foregroundStyle(.red)
                        .lineLimit(2)
                }
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityText)
    }

    private var accessibilityText: String {
        var text = "Rule \(position) of \(total): \(rule.id). \(actionSummary). When \(rule.when)."
        if !rule.valid {
            text += " Invalid"
            if let issue = rule.issue { text += ": \(issue.message)" }
            text += "."
        }
        return text
    }
}
