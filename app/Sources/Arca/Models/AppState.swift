import Foundation
import Combine

@MainActor
final class AppState: ObservableObject {
    @Published var selectedModel: String = ""
    @Published var watchedFolders: [URL] = []
    @Published var needsOnboarding: Bool = false

    private let defaults = UserDefaults.standard

    init() {
        selectedModel = defaults.string(forKey: "selectedModel") ?? ""
        needsOnboarding = selectedModel.isEmpty
        if let bookmarks = defaults.array(forKey: "watchedFolders") as? [Data] {
            watchedFolders = bookmarks.compactMap { data in
                var stale = false
                return try? URL(resolvingBookmarkData: data, options: .withSecurityScope, relativeTo: nil, bookmarkDataIsStale: &stale)
            }
        }
    }

    func selectModel(_ id: String) {
        selectedModel = id
        needsOnboarding = false
        defaults.set(id, forKey: "selectedModel")
    }

    func addWatchedFolder(_ url: URL) {
        guard !watchedFolders.contains(url) else { return }
        watchedFolders.append(url)
        saveWatchedFolders()
    }

    func removeWatchedFolder(_ url: URL) {
        watchedFolders.removeAll { $0 == url }
        saveWatchedFolders()
    }

    private func saveWatchedFolders() {
        let bookmarks = watchedFolders.compactMap { url in
            try? url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
        }
        defaults.set(bookmarks, forKey: "watchedFolders")
    }
}
