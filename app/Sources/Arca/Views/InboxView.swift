import SwiftUI
import AppKit

/// Suggestions for new files in the watched folders, waiting for the user,
/// and the moves made recently (which can be undone).
struct InboxView: View {
    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @State private var pending: [Decision] = []
    @State private var recent: [Decision] = []
    @State private var loading = false
    @State private var busyID: Int64?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
            if pending.isEmpty && recent.isEmpty && !loading {
                emptyState
            } else {
                List {
                    if !pending.isEmpty {
                        SwiftUI.Section("Waiting for you (\(pending.count))") {
                            ForEach(pending) { decision in
                                PendingRow(decision: decision, busy: busyID == decision.id,
                                           onMove: { Task { await accept(decision, target: nil) } },
                                           onChoose: { chooseFolder(for: decision) },
                                           onSkip: { Task { await reject(decision) } })
                            }
                        }
                    }
                    if !recent.isEmpty {
                        SwiftUI.Section("Recently moved") {
                            ForEach(recent) { decision in
                                RecentRow(decision: decision, busy: busyID == decision.id) {
                                    Task { await undo(decision) }
                                }
                            }
                        }
                    }
                }
                .listStyle(.inset)
            }
        }
        .task(id: appState.pendingCount) { await load() }
        .task(id: backend.isRunning) { await load() }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Inbox").font(.title2.bold())
                Text("Suggestions for new files in your watched folders. Nothing moves until you choose.")
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                    .font(.callout)
                    .lineLimit(2)
            }
            Button {
                Task { await load() }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .disabled(loading)
        }
        .padding(16)
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("Nothing to review", systemImage: "tray")
        } description: {
            if appState.watchedFolderPaths.isEmpty {
                Text("Add a folder in Settings, such as Downloads. When new files arrive there, Arca suggests where they belong.")
            } else {
                Text("When a new file lands in \(watchedNames), Arca suggests where it belongs and the suggestion appears here.")
            }
        } actions: {
            if appState.watchedFolderPaths.isEmpty {
                SettingsLink { Text("Open Settings…") }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var watchedNames: String {
        let names = appState.watchedFolderPaths.map { ($0 as NSString).lastPathComponent }
        return ListFormatter.localizedString(byJoining: names)
    }

    private func load() async {
        guard backend.isRunning else { return }
        loading = true
        defer { loading = false }
        do {
            pending = try await APIClient.shared.decisions(status: .pending, limit: 100).items
            // Accepted decisions keep "accepted" once moved; auto-applied
            // ones are "applied". Both carry the journal entry for undo.
            let page = try await APIClient.shared.decisions(limit: 100)
            recent = page.items
                .filter { ($0.status == .accepted || $0.status == .applied) && $0.journalID != nil }
                .prefix(15)
                .map { $0 }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func accept(_ decision: Decision, target: String?) async {
        busyID = decision.id
        defer { busyID = nil }
        do {
            _ = try await APIClient.shared.acceptDecision(id: decision.id, target: target)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        await load()
        await appState.decisionsChanged()
    }

    private func reject(_ decision: Decision) async {
        busyID = decision.id
        defer { busyID = nil }
        do {
            _ = try await APIClient.shared.rejectDecision(id: decision.id)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        await load()
        await appState.decisionsChanged()
    }

    private func undo(_ decision: Decision) async {
        guard let journalID = decision.journalID else { return }
        busyID = decision.id
        defer { busyID = nil }
        do {
            try await APIClient.shared.undo(journalID: journalID)
            recent.removeAll { $0.id == decision.id }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Lets the user pick another destination inside the same watched folder.
    private func chooseFolder(for decision: Decision) {
        let root = URL(fileURLWithPath: decision.root, isDirectory: true)
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.directoryURL = root
        panel.prompt = "Move Here"
        panel.message = "Choose where \(decision.fileName) should go (inside \(decision.folderName))"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        let rootPath = root.standardizedFileURL.path
        let chosen = url.standardizedFileURL.path
        guard chosen.hasPrefix(rootPath + "/") else {
            error = "Choose a folder inside \(decision.folderName)."
            return
        }
        let relative = String(chosen.dropFirst(rootPath.count + 1))
        Task { await accept(decision, target: relative) }
    }
}

private struct PendingRow: View {
    let decision: Decision
    let busy: Bool
    let onMove: () -> Void
    let onChoose: () -> Void
    let onSkip: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            Image(nsImage: NSWorkspace.shared.icon(forFile: decision.fileURL.path))
                .resizable()
                .frame(width: 32, height: 32)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                Text(decision.fileName)
                    .fontWeight(.medium)
                    .lineLimit(1)
                    .truncationMode(.middle)
                HStack(spacing: 4) {
                    Text(decision.action == "move" ? "Move to" : decision.action.capitalized)
                        .foregroundStyle(.secondary)
                    if decision.action == "move" {
                        Image(systemName: "folder").foregroundStyle(Color.accentColor)
                        Text(decision.target).foregroundStyle(Color.accentColor).fontWeight(.medium)
                    }
                    Text("in \(decision.folderName)").foregroundStyle(.secondary)
                }
                .font(.callout)
                Text(explanation)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Spacer()
            if busy {
                ProgressView().controlSize(.small)
            } else {
                Button("Move", action: onMove)
                    .buttonStyle(.borderedProminent)
                    .disabled(decision.action != "move")
                Menu {
                    Button("Choose Another Folder…", action: onChoose)
                    Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([decision.fileURL]) }
                    Divider()
                    Button("Leave It Where It Is", action: onSkip)
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .accessibilityLabel("More options")
                Button("Skip", action: onSkip)
            }
        }
        .padding(.vertical, 4)
    }

    private var explanation: String {
        let confidence = Int((decision.confidence * 100).rounded())
        var text = "Suggested by \(decision.sourceDescription) · \(confidence)% sure"
        if !decision.reason.isEmpty { text += " · \(decision.reason)" }
        return text
    }
}

private struct RecentRow: View {
    let decision: Decision
    let busy: Bool
    let onUndo: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
            VStack(alignment: .leading, spacing: 2) {
                Text(decision.fileName).lineLimit(1).truncationMode(.middle)
                Text("Moved to \(decision.target) in \(decision.folderName)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if let date = decision.date {
                Text(date.formatted(.relative(presentation: .named)))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if busy {
                ProgressView().controlSize(.small)
            } else {
                Button("Undo", action: onUndo)
            }
        }
        .padding(.vertical, 2)
    }
}
