import SwiftUI

struct OnboardingView: View {
    @EnvironmentObject private var appState: AppState
    @State private var models: [ModelStatus] = []
    @State private var downloadingID: String?
    @State private var downloadProgress: Double = 0
    @State private var downloadError: String?

    var body: some View {
        VStack(spacing: 24) {
            Image(systemName: "archivebox")
                .font(.system(size: 56))
                .foregroundStyle(Color.accentColor)

            Text("Welcome to Arca")
                .font(.largeTitle)
                .fontWeight(.bold)

            Text("Arca needs a local AI model to work.\nChoose one to download — it runs entirely on your Mac.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)

            if models.isEmpty {
                ProgressView("Loading available models…")
                    .task { await loadModels() }
            } else {
                VStack(spacing: 12) {
                    ForEach(models) { model in
                        OnboardingModelCard(
                            model: model,
                            isDownloading: downloadingID == model.id,
                            progress: downloadingID == model.id ? downloadProgress : 0,
                            error: downloadingID == model.id ? downloadError : nil,
                            onDownload: { Task { await downloadModel(model.id) } }
                        )
                    }
                }
                .frame(maxWidth: 380)
            }
        }
        .padding(40)
        .frame(minWidth: 500, minHeight: 480)
    }

    private func loadModels() async {
        models = (try? await APIClient.shared.models()) ?? []
    }

    private func downloadModel(_ id: String) async {
        downloadingID = id
        downloadProgress = 0
        downloadError = nil
        do {
            // Download the chosen LLM model.
            try await streamDownload(id: id)
            // Download the embedding model in the background (small, ~80 MB).
            try await streamDownload(id: "nomic-embed")
            // Tell the backend to activate both.
            try await APIClient.shared.activateModel(id: id)
            appState.selectModel(id)
        } catch {
            downloadError = error.localizedDescription
            downloadingID = nil
        }
    }

    private func streamDownload(id: String) async throws {
        for try await progress in APIClient.shared.downloadModel(id: id) {
            if let err = progress.error, !err.isEmpty {
                throw APIError.serverError(err)
            }
            if id == downloadingID {
                downloadProgress = progress.percent
            }
        }
    }
}

struct OnboardingModelCard: View {
    let model: ModelStatus
    let isDownloading: Bool
    let progress: Double
    let error: String?
    let onDownload: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                VStack(alignment: .leading) {
                    Text(model.name).fontWeight(.semibold)
                    Text(String(format: "%.1f GB download", model.sizeGb))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if isDownloading {
                    Text(String(format: "%.0f%%", progress))
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                } else {
                    Button("Download", action: onDownload)
                        .buttonStyle(.borderedProminent)
                }
            }
            if isDownloading {
                ProgressView(value: progress, total: 100)
            }
            if let error {
                Text(error).font(.caption).foregroundStyle(.red)
            }
        }
        .padding()
        .background(Color(nsColor: .controlBackgroundColor))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
