import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct TableChatView: View {
    @EnvironmentObject private var backend: BackendProcess
    @State private var tables: [TableInfo] = []
    @State private var question: String = ""
    @State private var result: QueryResult?
    @State private var isLoading = false
    @State private var importing = false
    @State private var error: String?
    @State private var dropTargeted = false

    private static let importTypes = ["csv", "tsv", "xlsx", "parquet", "json"]

    var body: some View {
        HSplitView {
            tableList
                .frame(minWidth: 200, idealWidth: 230, maxWidth: 320)
            questionPane
                .frame(minWidth: 420)
        }
        .task(id: backend.isRunning) { await loadTables() }
        .dropDestination(for: URL.self) { urls, _ in
            let files = urls.filter { Self.importTypes.contains($0.pathExtension.lowercased()) }
            guard !files.isEmpty else { return false }
            Task { await importFiles(files) }
            return true
        } isTargeted: { dropTargeted = $0 }
        .overlay {
            if dropTargeted {
                RoundedRectangle(cornerRadius: 12)
                    .stroke(Color.accentColor, style: StrokeStyle(lineWidth: 3, dash: [8]))
                    .padding(6)
                    .allowsHitTesting(false)
            }
        }
    }

    // MARK: - Tables

    private var tableList: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Tables").font(.headline)
                Spacer()
                if importing { ProgressView().controlSize(.small) }
                Button {
                    pickFiles()
                } label: {
                    Label("Import", systemImage: "plus")
                }
                .help("Import a CSV, Excel, Parquet or JSON file")
                .disabled(importing || !backend.isRunning)
            }
            .padding(12)
            Divider()
            if tables.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "tablecells.badge.ellipsis")
                        .font(.largeTitle)
                        .foregroundStyle(.secondary)
                    Text("Import a spreadsheet or drop one here.")
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                    Button("Import…") { pickFiles() }
                        .disabled(importing || !backend.isRunning)
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List(tables) { table in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(table.name).font(.body.monospaced()).fontWeight(.medium)
                        Text("\(table.rows) rows · \(table.columns.map(\.name).joined(separator: ", "))")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                    }
                    .help(table.source)
                    .contextMenu {
                        if !table.source.isEmpty {
                            Button("Show Original File") {
                                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: table.source)])
                            }
                            Button("Re-import") { Task { await importFiles([URL(fileURLWithPath: table.source)]) } }
                        }
                        Divider()
                        Button("Remove", role: .destructive) { Task { await drop(table) } }
                    }
                }
                .listStyle(.sidebar)
            }
        }
    }

    // MARK: - Questions

    private var questionPane: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Ask Spreadsheets").font(.title2.bold())
                Text("Ask in plain words. Arca writes a database query, runs it on this Mac and shows the result and the query.")
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                TextField(tables.isEmpty ? "Import a table first…" : "e.g. Total spent per category in February",
                          text: $question)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await runQuery() } }
                Button("Ask") { Task { await runQuery() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(question.trimmingCharacters(in: .whitespaces).isEmpty || isLoading || tables.isEmpty)
            }

            if isLoading {
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Writing and running the query…").foregroundStyle(.secondary)
                }
            } else if let error {
                ScrollView {
                    Label {
                        Text(error).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 160)
            }

            if let result, !isLoading {
                if let sql = result.sql, !sql.isEmpty {
                    DisclosureGroup("Query") {
                        Text(sql)
                            .font(.callout.monospaced())
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                            .background(RoundedRectangle(cornerRadius: 6).fill(Color(nsColor: .textBackgroundColor)))
                    }
                }
                ResultTable(result: result)
                if result.truncated == true {
                    Text("Showing the first \(result.rows.count) rows.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            } else if result == nil && error == nil && !isLoading {
                Spacer()
            }
            Spacer(minLength: 0)
        }
        .padding(16)
    }

    // MARK: - Actions

    private func loadTables() async {
        guard backend.isRunning else { return }
        do {
            tables = try await APIClient.shared.tables()
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func pickFiles() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = true
        panel.allowedContentTypes = Self.importTypes.compactMap { UTType(filenameExtension: $0) }
        panel.prompt = "Import"
        if panel.runModal() == .OK {
            let urls = panel.urls
            Task { await importFiles(urls) }
        }
    }

    private func importFiles(_ urls: [URL]) async {
        importing = true
        defer { importing = false }
        error = nil
        for url in urls {
            do {
                _ = try await APIClient.shared.importTable(path: url.path)
            } catch {
                self.error = "\(url.lastPathComponent): \(error.localizedDescription)"
            }
        }
        await loadTables()
    }

    private func drop(_ table: TableInfo) async {
        do {
            try await APIClient.shared.dropTable(name: table.name)
        } catch {
            self.error = error.localizedDescription
        }
        await loadTables()
    }

    private func runQuery() async {
        let q = question.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty, !isLoading else { return }
        isLoading = true
        error = nil
        defer { isLoading = false }
        do {
            result = try await APIClient.shared.query(question: q)
        } catch {
            result = nil
            self.error = error.localizedDescription
        }
    }
}

struct ResultTable: View {
    let result: QueryResult

    var body: some View {
        if result.columns.isEmpty || result.rows.isEmpty {
            Text("The query returned no rows.").foregroundStyle(.secondary)
        } else {
            ScrollView([.horizontal, .vertical]) {
                Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
                    GridRow {
                        ForEach(result.columns.indices, id: \.self) { col in
                            Text(result.columns[col])
                                .fontWeight(.semibold)
                                .padding(.horizontal, 10).padding(.vertical, 6)
                                .frame(minWidth: 90, alignment: .leading)
                        }
                    }
                    .background(Color(nsColor: .separatorColor).opacity(0.2))
                    ForEach(result.rows.indices, id: \.self) { row in
                        GridRow {
                            ForEach(result.columns.indices, id: \.self) { col in
                                Text(col < result.rows[row].count ? result.rows[row][col].display : "")
                                    .font(.body.monospacedDigit())
                                    .textSelection(.enabled)
                                    .padding(.horizontal, 10).padding(.vertical, 5)
                                    .frame(minWidth: 90, alignment: .leading)
                            }
                        }
                        .background(row % 2 == 0 ? Color.clear : Color(nsColor: .controlBackgroundColor))
                    }
                }
            }
            .background(Color(nsColor: .textBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color(nsColor: .separatorColor)))
        }
    }
}
