import SwiftUI
import AppKit

/// First-run setup: what Arca is, which folders it may read, which model to
/// download, and where to find things. Every step can be revisited later
/// from Settings or Help → Run Setup Again.
struct OnboardingView: View {
    enum Step: Int, CaseIterable {
        case welcome, folders, model, done
    }

    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @State private var step: Step = .welcome

    var body: some View {
        VStack(spacing: 0) {
            StepIndicator(current: step.rawValue, count: Step.allCases.count)
                .padding(.top, 24)

            Group {
                switch step {
                case .welcome: WelcomeStep { step = .folders }
                case .folders: FoldersStep(onBack: { step = .welcome }) { step = .model }
                case .model: ModelStep(onBack: { step = .folders }) { step = .done }
                case .done: DoneStep { appState.onboardingComplete = true }
                }
            }
            .frame(maxWidth: 560)
            .padding(.horizontal, 40)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .transition(.opacity)
            .animation(.easeInOut(duration: 0.2), value: step)

            if case .failed(let message) = backend.state {
                HStack {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    Text(message).font(.callout)
                    Spacer()
                    Button("Try Again") { Task { await backend.restart() } }
                }
                .padding(12)
                .background(.orange.opacity(0.1))
            }
        }
        .frame(minWidth: 700, minHeight: 560)
    }
}

private struct StepIndicator: View {
    let current: Int
    let count: Int

    var body: some View {
        HStack(spacing: 8) {
            ForEach(0..<count, id: \.self) { i in
                Capsule()
                    .fill(i <= current ? Color.accentColor : Color.secondary.opacity(0.25))
                    .frame(width: i == current ? 28 : 8, height: 8)
            }
        }
        .animation(.easeInOut(duration: 0.2), value: current)
        .accessibilityElement()
        .accessibilityLabel("Step \(current + 1) of \(count)")
    }
}

// MARK: - Welcome

private struct WelcomeStep: View {
    let onNext: () -> Void

    var body: some View {
        VStack(spacing: 22) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 96, height: 96)
                .accessibilityHidden(true)

            VStack(spacing: 6) {
                Text("Welcome to Arca")
                    .font(.largeTitle.bold())
                Text("Your private assistant for the files on this Mac.")
                    .font(.title3)
                    .foregroundStyle(.secondary)
            }

            VStack(alignment: .leading, spacing: 14) {
                Feature(systemImage: "tray.full", title: "Keeps folders tidy",
                        text: "Suggests where new files belong. Nothing moves until you say so, and every move can be undone.")
                Feature(systemImage: "doc.text.magnifyingglass", title: "Answers from your documents",
                        text: "Ask questions about your PDFs, Word files and notes and see which file the answer came from.")
                Feature(systemImage: "tablecells", title: "Understands spreadsheets",
                        text: "Ask in plain words: \u{201C}How much did I spend on groceries in March?\u{201D}")
            }
            .padding(.vertical, 4)

            Label {
                Text("Everything runs on this Mac. Your files never leave it; the internet is only used once, to download the AI model.")
            } icon: {
                Image(systemName: "lock.shield.fill").foregroundStyle(.green)
            }
            .font(.callout)
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color.green.opacity(0.08)))

            Button(action: onNext) {
                Text("Get Started").frame(minWidth: 140)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .keyboardShortcut(.defaultAction)
        }
    }
}

private struct Feature: View {
    let systemImage: String
    let title: String
    let text: String

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: systemImage)
                .font(.title2)
                .foregroundStyle(Color.accentColor)
                .frame(width: 30)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).fontWeight(.semibold)
                Text(text).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

// MARK: - Folders

private struct FoldersStep: View {
    let onBack: () -> Void
    let onNext: () -> Void

    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @State private var folders: [String] = []
    @State private var loaded = false
    @State private var saving = false
    @State private var error: String?

    private var suggestions: [URL] {
        let fm = FileManager.default
        return [FileManager.SearchPathDirectory.downloadsDirectory, .documentDirectory, .desktopDirectory]
            .compactMap { fm.urls(for: $0, in: .userDomainMask).first }
            .filter { !folders.contains($0.path) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            StepHeader(title: "Which folders should Arca look after?",
                       subtitle: "Arca reads the documents in these folders so you can ask about them, and suggests where new files should go. Choose your Downloads folder to start; you can change this any time in Settings.")

            VStack(spacing: 0) {
                if folders.isEmpty {
                    Text("No folders yet")
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, minHeight: 60)
                } else {
                    ForEach(folders, id: \.self) { path in
                        HStack {
                            Image(systemName: "folder.fill").foregroundStyle(Color.accentColor)
                            VStack(alignment: .leading) {
                                Text((path as NSString).lastPathComponent).fontWeight(.medium)
                                Text(path).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                            }
                            Spacer()
                            Button {
                                folders.removeAll { $0 == path }
                            } label: {
                                Image(systemName: "minus.circle.fill").foregroundStyle(.secondary)
                            }
                            .buttonStyle(.borderless)
                            .accessibilityLabel("Remove \((path as NSString).lastPathComponent)")
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        if path != folders.last { Divider() }
                    }
                }
            }
            .background(RoundedRectangle(cornerRadius: 10).fill(Color(nsColor: .controlBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color(nsColor: .separatorColor)))

            HStack {
                Button("Add Folder…") { pickFolder() }
                ForEach(suggestions, id: \.self) { url in
                    Button("+ \(url.lastPathComponent)") { folders.append(url.path) }
                }
            }

            Text("Arca never deletes files. When it moves one (only after you agree), the move is recorded and can be undone from the Inbox.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                    .font(.callout)
            }

            Spacer(minLength: 0)

            HStack {
                Button("Back", action: onBack)
                Spacer()
                if saving {
                    ProgressView().controlSize(.small)
                    Text("Saving…").foregroundStyle(.secondary)
                }
                Button(folders.isEmpty ? "Skip for Now" : "Continue") { Task { await save() } }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(saving || !backend.isRunning)
            }
        }
        .padding(.vertical, 28)
        .task {
            guard !loaded else { return }
            loaded = true
            if appState.settings == nil { await appState.refresh() }
            folders = appState.watchedFolderPaths
        }
    }

    private func pickFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = true
        panel.prompt = "Choose"
        panel.message = "Choose folders for Arca to look after"
        if panel.runModal() == .OK {
            for url in panel.urls where !folders.contains(url.path) {
                folders.append(url.path)
            }
        }
    }

    private func save() async {
        error = nil
        if folders == appState.watchedFolderPaths {
            onNext()
            return
        }
        saving = true
        defer { saving = false }
        do {
            try await appState.setWatchedFolders(folders)
            onNext()
        } catch {
            self.error = error.localizedDescription
        }
    }
}

// MARK: - Model

private struct ModelStep: View {
    let onBack: () -> Void
    let onNext: () -> Void

    @EnvironmentObject private var appState: AppState
    @EnvironmentObject private var backend: BackendProcess
    @State private var models: [ModelStatus] = []
    @State private var selected: String?
    @State private var phase: Phase = .choosing
    @State private var downloadTask: Task<Void, Never>?

    enum Phase: Equatable {
        case choosing
        case downloading(label: String, fraction: Double, detail: String)
        case activating
        case failed(String)
    }

    private var languageModels: [ModelStatus] { models.filter(\.isLanguageModel) }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            StepHeader(title: "Choose an AI model",
                       subtitle: "The model runs entirely on this Mac. It is downloaded once (about 2 GB) from Hugging Face, checked against a known fingerprint, and stored in Arca's data folder.")

            if languageModels.isEmpty {
                HStack {
                    ProgressView().controlSize(.small)
                    Text(backend.isRunning ? "Loading models…" : "Waiting for Arca's server…").foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 120)
            } else {
                VStack(spacing: 10) {
                    ForEach(languageModels) { model in
                        ModelCard(model: model, isSelected: selected == model.id, enabled: phase == .choosing || isFailed) {
                            selected = model.id
                        }
                    }
                }
            }

            switch phase {
            case .downloading(let label, let fraction, let detail):
                VStack(alignment: .leading, spacing: 6) {
                    Text(label).font(.callout)
                    ProgressView(value: fraction)
                    Text(detail).font(.caption).foregroundStyle(.secondary).monospacedDigit()
                }
            case .activating:
                HStack {
                    ProgressView().controlSize(.small)
                    Text("Starting the model…").foregroundStyle(.secondary)
                }
            case .failed(let message):
                Label(message, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
            case .choosing:
                Text("Not sure? Llama 3.2 is a good default. You can switch later in Settings.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Spacer(minLength: 0)

            HStack {
                Button("Back", action: onBack).disabled(isBusy)
                Spacer()
                if isBusy {
                    Button("Cancel") {
                        downloadTask?.cancel()
                        phase = .choosing
                    }
                }
                Button(primaryTitle) { start() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(selected == nil || isBusy || !backend.isRunning)
            }
        }
        .padding(.vertical, 28)
        .task(id: backend.isRunning) { await loadModels() }
        .onDisappear { downloadTask?.cancel() }
    }

    private var isBusy: Bool {
        switch phase {
        case .downloading, .activating: return true
        default: return false
        }
    }

    private var isFailed: Bool {
        if case .failed = phase { return true }
        return false
    }

    private var primaryTitle: String {
        guard let model = models.first(where: { $0.id == selected }) else { return "Download" }
        let embeddingReady = models.first(where: { $0.id == ModelStatus.embeddingID })?.downloaded ?? false
        if model.downloaded && embeddingReady { return model.active ? "Continue" : "Use This Model" }
        return "Download"
    }

    private func loadModels() async {
        guard backend.isRunning else { return }
        guard let list = try? await APIClient.shared.models() else { return }
        models = list
        if selected == nil {
            selected = list.first(where: { $0.active })?.id
                ?? list.first(where: { $0.isLanguageModel && $0.downloaded })?.id
                ?? list.first(where: \.isLanguageModel)?.id
        }
    }

    private func start() {
        guard let id = selected else { return }
        downloadTask = Task { await downloadAndActivate(id) }
    }

    private func downloadAndActivate(_ id: String) async {
        do {
            // The small search model first, so a failure shows up quickly.
            for model in [ModelStatus.embeddingID, id] {
                guard let status = models.first(where: { $0.id == model }), !status.downloaded else { continue }
                try await download(status)
                if Task.isCancelled { return }
            }
            phase = .activating
            if !(models.first(where: { $0.id == id })?.active ?? false) || appState.activeModel != id {
                try await appState.activateModel(id)
            }
            await loadModels()
            phase = .choosing
            onNext()
        } catch is CancellationError {
            phase = .choosing
        } catch {
            if Task.isCancelled { phase = .choosing; return }
            phase = .failed("The download did not finish: \(error.localizedDescription) Check your internet connection and try again.")
        }
    }

    private func download(_ model: ModelStatus) async throws {
        let label = model.isLanguageModel ? "Downloading \(model.name)…" : "Downloading the search model…"
        phase = .downloading(label: label, fraction: 0, detail: "Connecting…")
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        var finished = false
        for try await progress in APIClient.shared.downloadModel(id: model.id) {
            if let err = progress.error, !err.isEmpty { throw APIError.serverError(err) }
            let fraction = progress.total > 0 ? Double(progress.downloaded) / Double(progress.total) : 0
            let detail = progress.total > 0
                ? "\(formatter.string(fromByteCount: progress.downloaded)) of \(formatter.string(fromByteCount: progress.total))"
                : formatter.string(fromByteCount: progress.downloaded)
            phase = .downloading(label: label, fraction: min(max(fraction, 0), 1), detail: detail)
            if progress.done { finished = true }
        }
        try Task.checkCancellation()
        if !finished { throw APIError.serverError("The connection to the download was lost.") }
        await loadModels()
    }
}

private struct ModelCard: View {
    let model: ModelStatus
    let isSelected: Bool
    let enabled: Bool
    let onSelect: () -> Void

    var body: some View {
        Button(action: onSelect) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: isSelected ? "largecircle.fill.circle" : "circle")
                    .font(.title3)
                    .foregroundStyle(isSelected ? Color.accentColor : .secondary)
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(model.name).fontWeight(.semibold)
                        if model.active {
                            Text("Active").font(.caption2.bold()).padding(.horizontal, 6).padding(.vertical, 2)
                                .background(Capsule().fill(Color.green.opacity(0.2)))
                        } else if model.downloaded {
                            Text("Downloaded").font(.caption2.bold()).padding(.horizontal, 6).padding(.vertical, 2)
                                .background(Capsule().fill(Color.secondary.opacity(0.15)))
                        }
                    }
                    if !model.description.isEmpty {
                        Text(model.description).font(.callout).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer()
                Text(String(format: "%.1f GB", model.sizeGb))
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            .padding(14)
            .contentShape(Rectangle())
            .background(
                RoundedRectangle(cornerRadius: 10)
                    .fill(isSelected ? Color.accentColor.opacity(0.08) : Color(nsColor: .controlBackgroundColor))
            )
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(isSelected ? Color.accentColor : Color(nsColor: .separatorColor), lineWidth: isSelected ? 2 : 1)
            )
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }
}

// MARK: - Done

private struct DoneStep: View {
    let onFinish: () -> Void
    @EnvironmentObject private var appState: AppState

    var body: some View {
        VStack(spacing: 22) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 64))
                .foregroundStyle(.green)
                .accessibilityHidden(true)
            Text("You're all set")
                .font(.largeTitle.bold())

            if let status = appState.indexStatus, !appState.watchedFolderPaths.isEmpty {
                HStack(spacing: 8) {
                    if status.running { ProgressView().controlSize(.small) }
                    Text(status.summary).foregroundStyle(.secondary)
                }
            }

            VStack(alignment: .leading, spacing: 14) {
                Feature(systemImage: "tray.full", title: "Inbox",
                        text: "When a new file lands in a watched folder, Arca suggests where it belongs. Review suggestions here.")
                Feature(systemImage: "folder.badge.gearshape", title: "Tidy a Folder",
                        text: "Point Arca at a messy folder and it proposes a set of sub-folders for the loose files.")
                Feature(systemImage: "doc.text.magnifyingglass", title: "Ask Documents",
                        text: "Questions are answered from the documents in your watched folders. Reading them takes a few minutes the first time.")
                Feature(systemImage: "tablecells", title: "Ask Spreadsheets",
                        text: "Import a CSV or Excel file, then ask questions about it.")
            }

            Button(action: onFinish) {
                Text("Start Using Arca").frame(minWidth: 160)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .keyboardShortcut(.defaultAction)
        }
    }
}

private struct StepHeader: View {
    let title: String
    let subtitle: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.title.bold())
            Text(subtitle)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
