import SwiftUI

struct FileSorterView: View {
    @State private var folderPath: String = ""
    @State private var moves: [SortMove] = []
    @State private var isAnalysing = false
    @State private var isApplying = false
    @State private var applyResult: String?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("File Sorter")
                .font(.title2)
                .fontWeight(.semibold)

            HStack {
                TextField("Folder path", text: $folderPath)
                    .textFieldStyle(.roundedBorder)

                Button("Browse") { pickFolder() }

                Button("Analyse") {
                    Task { await analyse() }
                }
                .disabled(folderPath.isEmpty || isAnalysing)
            }

            if isAnalysing {
                ProgressView("Analysing folder…")
            } else if let err = error {
                Text(err).foregroundStyle(.red).font(.caption)
            } else if !moves.isEmpty {
                movesTable
                applyBar
            } else {
                ContentUnavailableView("Select a folder to get sorting suggestions",
                                       systemImage: "folder.badge.questionmark")
            }

            Spacer()
        }
        .padding()
        .frame(minWidth: 540, minHeight: 420)
    }

    // MARK: - Subviews

    private var movesTable: some View {
        VStack(spacing: 0) {
            HStack {
                Text("File").fontWeight(.semibold).frame(maxWidth: .infinity, alignment: .leading)
                Text("Move to").fontWeight(.semibold).frame(width: 200, alignment: .leading)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(Color(nsColor: .controlBackgroundColor))

            Divider()

            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(moves.enumerated()), id: \.offset) { idx, move in
                        HStack {
                            Text(move.file)
                                .font(.body.monospaced())
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            Image(systemName: "arrow.right")
                                .foregroundStyle(.secondary)
                                .font(.caption)
                            Text(move.suggestedFolder)
                                .font(.body.monospaced())
                                .foregroundStyle(Color.accentColor)
                                .lineLimit(1)
                                .frame(width: 200, alignment: .leading)
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 5)
                        .background(idx % 2 == 0 ? Color.clear : Color(nsColor: .controlBackgroundColor).opacity(0.4))
                    }
                }
            }
            .background(Color(nsColor: .textBackgroundColor))
        }
        .clipShape(RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color(nsColor: .separatorColor), lineWidth: 1))
    }

    private var applyBar: some View {
        HStack {
            Text("\(moves.count) moves suggested")
                .foregroundStyle(.secondary)
                .font(.caption)

            Spacer()

            if let result = applyResult {
                Text(result)
                    .foregroundStyle(.secondary)
                    .font(.caption)
            }

            Button(isApplying ? "Applying…" : "Apply All") {
                Task { await apply() }
            }
            .buttonStyle(.borderedProminent)
            .disabled(isApplying)
        }
    }

    // MARK: - Actions

    private func pickFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let url = panel.url {
            folderPath = url.path
            moves = []
            applyResult = nil
            error = nil
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
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func apply() async {
        isApplying = true
        defer { isApplying = false }
        do {
            let result = try await APIClient.shared.applySort(folder: folderPath, moves: moves)
            applyResult = "✓ \(result.applied) moved, \(result.skipped) skipped"
            moves = []
        } catch {
            self.error = error.localizedDescription
        }
    }
}
