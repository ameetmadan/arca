import Foundation
import Combine

/// App-wide state shared by the main window and Settings. The server owns
/// the real settings (config.yaml); this mirrors them and polls what changes
/// in the background (indexing progress, the Inbox count).
@MainActor
final class AppState: ObservableObject {
    @Published private(set) var settings: ServerSettings?
    @Published private(set) var indexStatus: IndexStatus?
    @Published private(set) var pendingCount = 0
    @Published var onboardingComplete: Bool {
        didSet { defaults.set(onboardingComplete, forKey: Self.onboardingKey) }
    }

    let backend: BackendProcess

    private let defaults = UserDefaults.standard
    private static let onboardingKey = "onboardingComplete"
    private var pollTask: Task<Void, Never>?

    init(backend: BackendProcess) {
        self.backend = backend
        onboardingComplete = UserDefaults.standard.bool(forKey: Self.onboardingKey)
    }

    /// Watched folders as URLs (the rules editor builds test facts from them).
    var watchedFolders: [URL] {
        (settings?.watchedFolders ?? []).map { URL(fileURLWithPath: $0, isDirectory: true) }
    }

    var watchedFolderPaths: [String] { settings?.watchedFolders ?? [] }

    var activeModel: String { settings?.activeModel ?? "" }

    private var launched = false

    /// Starts the server and background polling, once per app run.
    func launch() async {
        guard !launched else { return }
        launched = true
        await backend.start()
        await refresh()
        startPolling()
    }

    /// Fetches everything once. Failures leave the previous values in place.
    func refresh() async {
        if let s = try? await APIClient.shared.settings() { settings = s }
        await refreshActivity()
    }

    private func refreshActivity() async {
        if let status = try? await APIClient.shared.indexStatus() { indexStatus = status }
        if let page = try? await APIClient.shared.decisions(status: .pending, limit: 1) { pendingCount = page.total }
    }

    /// Polls indexing progress and the Inbox while the app runs: quickly
    /// while a scan is running, slowly otherwise.
    func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                if self.backend.isRunning {
                    if self.settings == nil { await self.refresh() } else { await self.refreshActivity() }
                }
                let busy = self.indexStatus?.running ?? false
                try? await Task.sleep(for: .seconds(busy ? 1 : 4))
            }
        }
    }

    /// Saves the watched folders and restarts the server so every component
    /// picks them up.
    func setWatchedFolders(_ paths: [String]) async throws {
        _ = try await APIClient.shared.setWatchedFolders(paths)
        await backend.restart()
        await refresh()
    }

    func addWatchedFolder(_ url: URL) async throws {
        let path = url.standardizedFileURL.path
        guard !watchedFolderPaths.contains(path) else { return }
        try await setWatchedFolders(watchedFolderPaths + [path])
    }

    func removeWatchedFolder(_ path: String) async throws {
        try await setWatchedFolders(watchedFolderPaths.filter { $0 != path })
    }

    /// Activates a downloaded model and restarts the server so indexing and
    /// the folder index start using the embedding model straight away.
    func activateModel(_ id: String) async throws {
        try await APIClient.shared.activateModel(id: id)
        await backend.restart()
        await refresh()
    }

    func rescan() async {
        try? await APIClient.shared.rescan()
        await refreshActivity()
    }

    func decisionsChanged() async {
        await refreshActivity()
    }
}
