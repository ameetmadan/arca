import SwiftUI

/// Optional, keyboard-accessible review. Loading and skipping never write data.
struct CalibrationView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var items: [CalibrationItem] = []
    @State private var loading = true
    @State private var busy = false
    @State private var error: String?
    @State private var position = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("Improve Suggestions").font(.title2.bold())
            Text("Confirm a few uncertain files. You can skip any file or stop at any time.")
                .foregroundStyle(.secondary)
            if let error { Text(error).foregroundStyle(.red) }
            if loading {
                ProgressView("Finding uncertain files…")
            } else if position < items.count {
                let item = items[position]
                Text("File \(position + 1) of \(items.count)").font(.caption)
                Text("Where does this belong?").font(.headline)
                Text(item.file).textSelection(.enabled)
                ForEach(Array(item.candidates.enumerated()), id: \.element.id) { index, candidate in
                    Button(candidate.target) { Task { await answer(item, target: candidate.target) } }
                        .keyboardShortcut(KeyEquivalent(Character(String(index + 1))), modifiers: [])
                        .accessibilityLabel("Move or confirm \(item.file) in \(candidate.target)")
                        .disabled(busy)
                }
                Button("Skip This File") { position += 1; error = nil }
                    .keyboardShortcut("s", modifiers: [])
                    .disabled(busy)
                if busy { ProgressView() }
            } else {
                Text(items.isEmpty ? "No uncertain files to review yet." : "All done. Thanks for improving your suggestions.")
            }
            HStack {
                Spacer()
                Button("Done") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                    .disabled(busy)
            }
        }
        .padding(28)
        .frame(width: 520, alignment: .leading)
        .task {
            do { items = try await APIClient.shared.calibration().items }
            catch { self.error = error.localizedDescription }
            loading = false
        }
    }

    private func answer(_ item: CalibrationItem, target: String) async {
        busy = true
        defer { busy = false }
        do {
            let decision = try await APIClient.shared.prepareCalibration(item)
            _ = try await APIClient.shared.acceptDecision(id: decision.id, target: target)
            position += 1
            error = nil
        } catch { self.error = error.localizedDescription }
    }
}
