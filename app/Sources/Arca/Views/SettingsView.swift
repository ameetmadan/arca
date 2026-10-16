import SwiftUI
import AppKit

struct SettingsView: View {
    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @State private var models: [ModelStatus] = []
    @State private var policies: [String: FolderPolicy] = [:]
    @State private var downloadingID: String?
    @State private var downloadProgress: Double = 0
    @State private var busy = false
    @State private var error: String?
    @State private var autoFolder: String?
    @State private var confirmAuto = false
    @State private var showCalibration = false

    var body: some View {
        Form {
            if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
            }

            Section {
                if appState.watchedFolderPaths.isEmpty {
                    Text("No folders yet. Add one, such as Downloads.").foregroundStyle(.secondary)
                }
                ForEach(appState.watchedFolderPaths, id: \.self) { path in
                    FolderRow(path: path, policy: policies[path], disabled: busy,
                              onMode: { mode in
                                  if mode == .auto { autoFolder = path; confirmAuto = true }
                                  else { Task { await setMode(mode, for: path) } }
                              },
                              onRemove: { Task { await removeFolder(path) } })
                }
                HStack {
                    Button("Add Folder…") { pickFolder() }.disabled(busy || !backend.isRunning)
                    if busy { ProgressView().controlSize(.small) }
                }
            } header: {
                Text("Watched Folders")
            } footer: {
                Text("Suggest: new files get a suggestion in the Inbox. Automatic: confident suggestions are applied without asking, once you've accepted enough of them. Off: the folder is only searched.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("AI Model") {
                if models.isEmpty {
                    ProgressView("Loading models…")
                } else {
                    ForEach(models.filter(\.isLanguageModel)) { model in
                        ModelRow(
                            model: model,
                            isDownloading: downloadingID == model.id,
                            progress: downloadingID == model.id ? downloadProgress : 0,
                            disabled: downloadingID != nil || busy,
                            onUse: { Task { await use(model) } },
                            onDownload: { Task { await download(model) } }
                        )
                    }
                    if let embedding = models.first(where: { !$0.isLanguageModel }) {
                        LabeledContent("Search model") {
                            Text(embedding.downloaded ? "Installed" : "Downloads with the first model")
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }

            Section("Search Index") {
                Button("Improve Suggestions…") { showCalibration = true }
                    .disabled(!backend.isRunning)
                LabeledContent("Status") {
                    Text(appState.indexStatus?.summary ?? "—").foregroundStyle(.secondary)
                }
                Button("Re-read Documents Now") { Task { await appState.rescan() } }
                    .disabled(!backend.isRunning || appState.indexStatus?.running == true)
            }

            Section("Privacy & Data") {
                Text("Arca runs entirely on this Mac. Your documents, the search index and the AI model are stored locally and never uploaded.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button("Show Data Folder") { NSWorkspace.shared.open(BackendProcess.dataURL) }
                    Button("Show Server Log") { NSWorkspace.shared.activateFileViewerSelecting([BackendProcess.logURL]) }
                    Button("Restart Server") { Task { await backend.restart(); await appState.refresh() } }
                }
            }

            Section("About") {
                LabeledContent("App version", value: appVersion)
                LabeledContent("Server version", value: appState.settings?.version ?? "—")
                Button("Run Setup Again…") { appState.onboardingComplete = false }
            }
        }
        .sheet(isPresented: $showCalibration) { CalibrationView() }
        .confirmationDialog("Allow automatic file moves?", isPresented: $confirmAuto) {
            Button("Enable Automatic Moves") {
                if let path = autoFolder { Task { await setMode(.auto, for: path) } }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Arca will move confident suggestions in this folder without asking. You can undo recent moves in the Inbox.")
        }
        .formStyle(.grouped)
        .frame(width: 560, height: 620)
        .task(id: backend.isRunning) { await load() }
    }

    private var appVersion: String {
        let info = Bundle.main.infoDictionary
        let v = info?["CFBundleShortVersionString"] as? String ?? "dev"
        let b = info?["CFBundleVersion"] as? String ?? ""
        return b.isEmpty ? v : "\(v) (\(b))"
    }

    private func load() async {
        guard backend.isRunning else { return }
        await appState.refresh()
        if let list = try? await APIClient.shared.models() { models = list }
        var found: [String: FolderPolicy] = [:]
        for path in appState.watchedFolderPaths {
            if let p = try? await APIClient.shared.folderPolicy(root: path) { found[path] = p }
        }
        policies = found
    }

    private func pickFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = true
        guard panel.runModal() == .OK else { return }
        let paths = panel.urls.map(\.path).filter { !appState.watchedFolderPaths.contains($0) }
        guard !paths.isEmpty else { return }
        Task { await save(appState.watchedFolderPaths + paths) }
    }

    private func removeFolder(_ path: String) async {
        await save(appState.watchedFolderPaths.filter { $0 != path })
    }

    private func save(_ folders: [String]) async {
        busy = true
        defer { busy = false }
        do {
            try await appState.setWatchedFolders(folders)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        await load()
    }

    private func setMode(_ mode: FolderMode, for path: String) async {
        do {
            policies[path] = try await APIClient.shared.setFolderPolicy(root: path, mode: mode)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func use(_ model: ModelStatus) async {
        busy = true
        defer { busy = false }
        do {
            try await appState.activateModel(model.id)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        await load()
    }

    private func download(_ model: ModelStatus) async {
        downloadingID = model.id
        downloadProgress = 0
        defer { downloadingID = nil }
        do {
            var ids = [model.id]
            if !(models.first(where: { $0.id == ModelStatus.embeddingID })?.downloaded ?? true) {
                ids.insert(ModelStatus.embeddingID, at: 0)
            }
            for id in ids {
                var finished = false
                for try await progress in APIClient.shared.downloadModel(id: id) {
                    if let err = progress.error, !err.isEmpty { throw APIError.serverError(err) }
                    if id == model.id { downloadProgress = progress.percent }
                    if progress.done { finished = true }
                }
                if !finished { throw APIError.serverError("The download was interrupted.") }
            }
            error = nil
        } catch {
            self.error = "Download failed: \(error.localizedDescription)"
        }
        await load()
    }
}

private struct FolderRow: View {
    let path: String
    let policy: FolderPolicy?
    let disabled: Bool
    let onMode: (FolderMode) -> Void
    let onRemove: () -> Void

    var body: some View {
        HStack {
            Image(systemName: "folder.fill").foregroundStyle(Color.accentColor)
            VStack(alignment: .leading) {
                Text((path as NSString).lastPathComponent)
                Text(path).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
            }
            Spacer()
            if let policy {
                Picker("Mode", selection: Binding(get: { policy.mode }, set: onMode)) {
                    ForEach(FolderMode.allCases) { mode in
                        Text(mode.title).tag(mode)
                            .disabled(mode == .auto && !policy.autoEligible && policy.mode != .auto)
                    }
                }
                .labelsHidden()
                .fixedSize()
                .help(policy.autoEligible ? "" : "Automatic becomes available after you've reviewed about 20 suggestions in this folder.")
            }
            Button(role: .destructive, action: onRemove) {
                Image(systemName: "minus.circle")
            }
            .buttonStyle(.borderless)
            .disabled(disabled)
            .accessibilityLabel("Stop watching \((path as NSString).lastPathComponent)")
        }
    }
}

struct ModelRow: View {
    let model: ModelStatus
    let isDownloading: Bool
    let progress: Double
    let disabled: Bool
    let onUse: () -> Void
    let onDownload: () -> Void

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(model.name).fontWeight(model.active ? .semibold : .regular)
                Text(String(format: "%.1f GB · ", model.sizeGb) + model.description)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            if model.active {
                Label("Active", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
                    .font(.caption)
            } else if isDownloading {
                ProgressView(value: progress, total: 100).frame(width: 110)
            } else if model.downloaded {
                Button("Use", action: onUse).disabled(disabled)
            } else {
                Button("Download", action: onDownload).disabled(disabled)
            }
        }
    }
}
