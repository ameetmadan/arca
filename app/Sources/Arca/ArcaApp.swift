import SwiftUI
import AppKit

@main
struct ArcaApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var backend: BackendProcess
    @StateObject private var appState: AppState

    init() {
        let backend = BackendProcess()
        _backend = StateObject(wrappedValue: backend)
        _appState = StateObject(wrappedValue: AppState(backend: backend))
    }

    var body: some Scene {
        WindowGroup("Arca") {
            RootView()
                .environmentObject(appState)
                .environmentObject(backend)
                .task { await appState.launch() }
        }
        .defaultSize(width: 980, height: 680)
        .commands {
            CommandGroup(replacing: .appInfo) {
                Button("About Arca") {
                    NSApp.orderFrontStandardAboutPanel(options: [
                        .credits: NSAttributedString(
                            string: "A private file assistant. Everything runs on this Mac.",
                            attributes: [.font: NSFont.systemFont(ofSize: 11)]
                        )
                    ])
                }
            }
            CommandGroup(after: .help) {
                Button("Run Setup Again…") { appState.onboardingComplete = false }
                Button("Show Server Log") { NSWorkspace.shared.activateFileViewerSelecting([BackendProcess.logURL]) }
            }
        }

        Settings {
            SettingsTabsView()
                .environmentObject(appState)
                .environmentObject(backend)
        }
    }
}

/// Quits the app when its window closes. (The backend stops itself on
/// NSApplication.willTerminateNotification; see BackendProcess.)
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }
}

struct RootView: View {
    @State private var showCalibration = false
    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess

    var body: some View {
        Group {
            if !appState.onboardingComplete {
                OnboardingView()
            } else {
                MainView()
            }
        }
        .frame(minWidth: 760, minHeight: 540)
        .alert("Improve folder suggestions?", isPresented: $appState.offerCalibration) {
            Button("Maybe Later", role: .cancel) {}
            Button("Review a Few Files") { showCalibration = true }
        } message: {
            Text("Optional: confirm where a few uncertain files belong. You can also do this from Settings.")
        }
        .sheet(isPresented: $showCalibration) { CalibrationView() }
    }
}

enum SidebarItem: String, CaseIterable, Identifiable, Hashable {
    case inbox, sorter, documents, tables

    var id: String { rawValue }

    var title: String {
        switch self {
        case .inbox: return "Inbox"
        case .sorter: return "Tidy a Folder"
        case .documents: return "Ask Documents"
        case .tables: return "Ask Spreadsheets"
        }
    }

    var systemImage: String {
        switch self {
        case .inbox: return "tray.full"
        case .sorter: return "folder.badge.gearshape"
        case .documents: return "doc.text.magnifyingglass"
        case .tables: return "tablecells"
        }
    }
}

struct MainView: View {
    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @SceneStorage("section") private var selection: SidebarItem = .documents

    var body: some View {
        NavigationSplitView {
            List(selection: Binding(get: { selection }, set: { if let s = $0 { selection = s } })) {
                ForEach(SidebarItem.allCases) { section in
                    Label(section.title, systemImage: section.systemImage)
                        .badge(section == .inbox ? appState.pendingCount : 0)
                        .tag(section)
                }
            }
            .navigationSplitViewColumnWidth(min: 190, ideal: 210)
            .safeAreaInset(edge: .bottom) { StatusFooter() }
        } detail: {
            VStack(spacing: 0) {
                if let banner = setupBanner {
                    banner
                    Divider()
                }
                detail
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    @ViewBuilder
    private var detail: some View {
        if case .failed(let message) = backend.state {
            BackendErrorView(message: message)
        } else {
            switch selection {
            case .inbox: InboxView()
            case .sorter: FileSorterView()
            case .documents: DocumentChatView()
            case .tables: TableChatView()
            }
        }
    }

    /// A reminder when setup was skipped or the model files went missing.
    private var setupBanner: AnyView? {
        guard backend.isRunning, let settings = appState.settings, !settings.modelsReady else { return nil }
        return AnyView(
            HStack(spacing: 10) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                Text(settings.activeModel.isEmpty
                     ? "No AI model is active yet, so Arca can't answer questions or suggest folders."
                     : "The search model is missing, so documents can't be searched.")
                Spacer()
                Button("Finish Setup…") { appState.onboardingComplete = false }
            }
            .font(.callout)
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .background(.orange.opacity(0.08))
        )
    }
}

/// Server and indexing status at the bottom of the sidebar.
struct StatusFooter: View {
    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Divider()
            HStack(spacing: 6) {
                Circle()
                    .fill(dotColor)
                    .frame(width: 8, height: 8)
                    .accessibilityHidden(true)
                Text(statusText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                Spacer(minLength: 0)
                if appState.indexStatus?.running == true {
                    ProgressView().controlSize(.mini)
                }
            }
            .padding(.horizontal, 12)
            .padding(.bottom, 8)
            .accessibilityElement(children: .combine)
        }
    }

    private var dotColor: Color {
        switch backend.state {
        case .running: return .green
        case .starting: return .yellow
        case .stopped: return .gray
        case .failed: return .red
        }
    }

    private var statusText: String {
        switch backend.state {
        case .starting: return "Starting…"
        case .stopped: return "Restarting…"
        case .failed: return "Server stopped"
        case .running:
            if appState.watchedFolderPaths.isEmpty { return "No folders watched" }
            return appState.indexStatus?.summary ?? "Ready"
        }
    }
}

/// Shown instead of a section when the backend could not be started.
struct BackendErrorView: View {
    let message: String
    @EnvironmentObject private var backend: BackendProcess

    var body: some View {
        ContentUnavailableView {
            Label("Arca's background server isn't running", systemImage: "bolt.horizontal.circle")
        } description: {
            Text(message)
        } actions: {
            HStack {
                Button("Try Again") { Task { await backend.restart() } }
                    .buttonStyle(.borderedProminent)
                Button("Show Log") { NSWorkspace.shared.activateFileViewerSelecting([BackendProcess.logURL]) }
            }
        }
    }
}
