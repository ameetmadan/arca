import SwiftUI

@main
struct ArcaApp: App {
    @StateObject private var backend = BackendProcess()
    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .task { await backend.start() }
        }
        .commands {
            CommandGroup(replacing: .appInfo) {
                Button("About Arca") { }
            }
        }

        Settings {
            SettingsTabsView()
                .environmentObject(appState)
        }
    }
}

struct ContentView: View {
    @EnvironmentObject private var appState: AppState

    var body: some View {
        if appState.needsOnboarding {
            OnboardingView()
                .environmentObject(appState)
        } else {
            MainView()
                .environmentObject(appState)
        }
    }
}

struct MainView: View {
    var body: some View {
        TabView {
            FileSorterView()
                .tabItem { Label("File Sorter", systemImage: "folder.badge.gearshape") }

            DocumentChatView()
                .tabItem { Label("Document Chat", systemImage: "doc.text.magnifyingglass") }

            TableChatView()
                .tabItem { Label("Table Chat", systemImage: "tablecells") }
        }
    }
}
