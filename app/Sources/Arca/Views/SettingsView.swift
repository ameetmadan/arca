import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var models: [ModelStatus] = []
    @State private var downloadingID: String?
    @State private var downloadProgress: Double = 0

    var body: some View {
        Form {
            Section("Model") {
                if models.isEmpty {
                    ProgressView("Loading models…")
                        .task { await loadModels() }
                } else {
                    ForEach(models) { model in
                        ModelRow(
                            model: model,
                            isSelected: appState.selectedModel == model.id,
                            isDownloading: downloadingID == model.id,
                            progress: downloadingID == model.id ? downloadProgress : 0,
                            onSelect: { appState.selectModel(model.id) },
                            onDownload: { Task { await downloadModel(model.id) } }
                        )
                    }
                }
            }

            Section("Watched Folders") {
                ForEach(appState.watchedFolders, id: \.self) { url in
                    HStack {
                        Text(url.path).truncationMode(.head)
                        Spacer()
                        Button(role: .destructive) {
                            appState.removeWatchedFolder(url)
                        } label: {
                            Image(systemName: "minus.circle")
                        }
                        .buttonStyle(.borderless)
                    }
                }

                Button("Add Folder…") { pickFolder() }
            }
        }
        .formStyle(.grouped)
        .frame(width: 480)
    }

    private func loadModels() async {
        models = (try? await APIClient.shared.models()) ?? []
    }

    private func downloadModel(_ id: String) async {
        downloadingID = id
        downloadProgress = 0
        defer { downloadingID = nil }
        do {
            for try await progress in APIClient.shared.downloadModel(id: id) {
                downloadProgress = progress.percent
                if progress.done {
                    await loadModels()
                }
            }
        } catch {
            // download error handled via UI state reset
        }
    }

    private func pickFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        if panel.runModal() == .OK, let url = panel.url {
            appState.addWatchedFolder(url)
        }
    }
}

struct ModelRow: View {
    let model: ModelStatus
    let isSelected: Bool
    let isDownloading: Bool
    let progress: Double
    let onSelect: () -> Void
    let onDownload: () -> Void

    var body: some View {
        HStack {
            VStack(alignment: .leading) {
                Text(model.name).fontWeight(isSelected ? .semibold : .regular)
                Text(String(format: "%.1f GB", model.sizeGb)).font(.caption).foregroundStyle(.secondary)
            }

            Spacer()

            if model.downloaded {
                if isSelected {
                    Label("Active", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .font(.caption)
                } else {
                    Button("Use", action: onSelect).buttonStyle(.bordered)
                }
            } else if isDownloading {
                ProgressView(value: progress, total: 100)
                    .frame(width: 120)
            } else {
                Button("Download", action: onDownload).buttonStyle(.bordered)
            }
        }
    }
}
