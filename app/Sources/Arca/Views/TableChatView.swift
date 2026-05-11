import SwiftUI

struct TableChatView: View {
    @State private var question: String = ""
    @State private var schemaHint: String = ""
    @State private var result: QueryResult?
    @State private var isLoading = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Table Chat")
                .font(.title2)
                .fontWeight(.semibold)

            GroupBox("Schema Hint (optional)") {
                TextEditor(text: $schemaHint)
                    .font(.body.monospaced())
                    .frame(height: 80)
            }

            HStack {
                TextField("Ask a question about your data…", text: $question)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await runQuery() } }

                Button("Run") { Task { await runQuery() } }
                    .disabled(question.isEmpty || isLoading)
            }

            if isLoading {
                ProgressView("Running query…")
            } else if let err = error {
                Text(err).foregroundStyle(.red).font(.caption)
            } else if let result {
                ResultTable(result: result)
            } else {
                ContentUnavailableView("Ask a question to query your tables",
                                       systemImage: "tablecells.badge.ellipsis")
            }

            Spacer()
        }
        .padding()
        .frame(minWidth: 600, minHeight: 450)
    }

    private func runQuery() async {
        isLoading = true
        error = nil
        defer { isLoading = false }
        do {
            result = try await APIClient.shared.query(question: question, schemaHint: schemaHint)
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct ResultTable: View {
    let result: QueryResult

    var body: some View {
        if result.columns.isEmpty {
            Text("No results").foregroundStyle(.secondary)
        } else {
            ScrollView([.horizontal, .vertical]) {
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: 0) {
                        ForEach(result.columns, id: \.self) { col in
                            Text(col)
                                .fontWeight(.semibold)
                                .frame(minWidth: 120, maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, 8).padding(.vertical, 4)
                                .background(Color(nsColor: .separatorColor).opacity(0.15))
                        }
                    }
                    Divider()
                    ForEach(Array(result.rows.indices), id: \.self) { rowIdx in
                        HStack(spacing: 0) {
                            ForEach(Array(result.columns.indices), id: \.self) { colIdx in
                                let cell = colIdx < result.rows[rowIdx].count
                                    ? "\(result.rows[rowIdx][colIdx].value ?? "")" : ""
                                Text(cell)
                                    .font(.body.monospaced())
                                    .frame(minWidth: 120, maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 8).padding(.vertical, 4)
                                    .background(rowIdx % 2 == 0 ? Color.clear : Color(nsColor: .controlBackgroundColor))
                            }
                        }
                        Divider()
                    }
                }
            }
            .background(Color(nsColor: .textBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: 8))
        }
    }
}
