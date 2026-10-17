import SwiftUI
import AppKit

/// Proposes sub-folders for the loose files in one folder, grouping files
/// with similar content. The user picks which moves to make.
struct FileSorterView: View {
    @EnvironmentObject private var appState: AppState
    @State private var folderPath: String = ""
    @State private var moves: [SortMove] = []
    @State private var selected: Set<SortMove> = []
    @State private var isAnalysing = false
    @State private var isApplying = false
    @State private var applyResult: String?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Tidy a Folder").font(.title2.bold())
                Text("Arca reads the loose files in a folder and proposes sub-folders for files that belong together. Files already in sub-folders are left alone. You choose which moves to make; Arca never overwrites an existing file.")
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                Menu {
                    ForEach(appState.watchedFolderPaths, id: \.self) { path in
                        Button(path) { choose(path) }
                    }
                    if !appState.watchedFolderPaths.isEmpty { Divider() }
                    Button("Other Folder…") { pickFolder() }
                } label: {
                    Label(folderPath.isEmpty ? "Choose a Folder" : (folderPath as NSString).lastPathComponent,
                          systemImage: "folder")
                }
                .fixedSize()

                if !folderPath.isEmpty {
                    Text(folderPath)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.head)
                }
                Spacer()
                Button(isAnalysing ? "Analysing…" : "Suggest Folders") {
                    Task { await analyse() }
                }
                .buttonStyle(.borderedProminent)
                .disabled(folderPath.isEmpty || isAnalysing || isApplying)
            }

            if isAnalysing {
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Reading files and naming groups. This can take a minute.")
                        .foregroundStyle(.secondary)
                }
            } else if let err = error {
                Label(err, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
            } else if let result = applyResult, moves.isEmpty {
                Label(result, systemImage: "checkmark.circle.fill").foregroundStyle(.green)
            }

            if !moves.isEmpty {
                movesTable
                applyBar
            } else if !isAnalysing {
                emptyState
            }
        }
        .padding(16)
    }

    // MARK: - Subviews

    /// Not a ContentUnavailableView: that view is meant to fill its whole
    /// container, and as a sibling in this VStack it painted over the header,
    /// the folder picker and the sidebar.
    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "folder.badge.questionmark")
                .font(.system(size: 40))
                .foregroundStyle(.tertiary)
                .accessibilityHidden(true)
            Text(folderPath.isEmpty ? "Choose a folder to get suggestions" : "Choose Suggest Folders to see proposed moves")
                .font(.headline)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var movesTable: some View {
        Table(moves) {
            TableColumn("") { move in
                Toggle("", isOn: Binding(
                    get: { selected.contains(move) },
                    set: { on in if on { selected.insert(move) } else { selected.remove(move) } }
                ))
                .labelsHidden()
                .accessibilityLabel("Move \(move.file)")
            }
            .width(24)
            TableColumn("File") { move in
                Text(move.file).lineLimit(1).truncationMode(.middle)
            }
            TableColumn("Move to") { move in
                Label(move.suggestedFolder, systemImage: "folder")
                    .foregroundStyle(Color.accentColor)
            }
            .width(min: 140, ideal: 200)
        }
        .frame(minHeight: 200)
    }

    private var applyBar: some View {
        HStack {
            Button(selected.count == moves.count ? "Select None" : "Select All") {
                selected = selected.count == moves.count ? [] : Set(moves)
            }
            Text("\(selected.count) of \(moves.count) selected")
                .foregroundStyle(.secondary)
                .font(.caption)
            Spacer()
            Button(isApplying ? "Moving…" : "Move \(selected.count) File\(selected.count == 1 ? "" : "s")") {
                Task { await apply() }
            }
            .buttonStyle(.borderedProminent)
            .disabled(isApplying || selected.isEmpty)
        }
    }

    // MARK: - Actions

    private func choose(_ path: String) {
        folderPath = path
        moves = []
        selected = []
        applyResult = nil
        error = nil
    }

    private func pickFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let url = panel.url {
            choose(url.path)
        }
    }

    private func analyse() async {
        isAnalysing = true
        error = nil
        applyResult = nil
        defer { isAnalysing = false }
        do {
            let result = try await APIClient.shared.sort(folder: folderPath)
            moves = result.moves
            selected = Set(result.moves)
            if moves.isEmpty {
                applyResult = "Nothing to tidy: there are no loose documents Arca can read in this folder."
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func apply() async {
        isApplying = true
        defer { isApplying = false }
        let chosen = moves.filter { selected.contains($0) }
        do {
            let result = try await APIClient.shared.applySort(folder: folderPath, moves: chosen)
            var text = "Moved \(result.applied) file\(result.applied == 1 ? "" : "s")"
            if result.skipped > 0 { text += ", skipped \(result.skipped)" }
            if let errors = result.errors, !errors.isEmpty {
                error = errors.joined(separator: "\n")
            }
            applyResult = text + "."
            moves = []
            selected = []
        } catch {
            self.error = error.localizedDescription
        }
    }
}

extension SortMove: Identifiable {
    var id: String { file }
}
