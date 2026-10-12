import SwiftUI

/// Content of the Settings window: the existing settings plus the rules editor.
struct SettingsTabsView: View {
    var body: some View {
        TabView {
            SettingsView()
                .tabItem { Label("General", systemImage: "gearshape") }

            RulesView()
                .tabItem { Label("Rules", systemImage: "list.bullet.rectangle") }
        }
    }
}
