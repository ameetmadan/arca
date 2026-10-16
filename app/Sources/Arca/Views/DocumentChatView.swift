import SwiftUI
import AppKit

struct DocumentChatView: View {
    @EnvironmentObject private var appState: AppState
    @State private var question: String = ""
    @State private var messages: [ChatMessage] = []
    @State private var streamTask: Task<Void, Never>?
    @FocusState private var inputFocused: Bool

    private var isStreaming: Bool { streamTask != nil }

    private let examples = [
        "What is the notice period in my lease?",
        "Summarise the latest invoice from my electricity provider",
        "When does my passport expire?",
    ]

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Ask Documents").font(.title2.bold())
                    Text(subtitle).foregroundStyle(.secondary)
                }
                Spacer()
                if !messages.isEmpty {
                    Button("New Chat") {
                        streamTask?.cancel()
                        messages = []
                    }
                    .disabled(isStreaming)
                }
            }
            .padding(16)

            Divider()

            if messages.isEmpty {
                emptyState
            } else {
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 14) {
                            ForEach(messages) { msg in
                                ChatBubble(message: msg).id(msg.id)
                            }
                        }
                        .padding(16)
                    }
                    .onChange(of: messages.last?.text) { _, _ in
                        if let last = messages.last { proxy.scrollTo(last.id, anchor: .bottom) }
                    }
                }
            }

            Divider()

            HStack(spacing: 8) {
                TextField("Ask about your documents…", text: $question, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(1...4)
                    .focused($inputFocused)
                    .onSubmit { send() }
                    .disabled(isStreaming)

                if isStreaming {
                    Button {
                        streamTask?.cancel()
                    } label: {
                        Image(systemName: "stop.circle.fill").font(.title2)
                    }
                    .buttonStyle(.borderless)
                    .help("Stop")
                    .accessibilityLabel("Stop answering")
                } else {
                    Button {
                        send()
                    } label: {
                        Image(systemName: "arrow.up.circle.fill").font(.title2)
                    }
                    .buttonStyle(.borderless)
                    .disabled(question.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .keyboardShortcut(.return, modifiers: .command)
                    .accessibilityLabel("Send question")
                }
            }
            .padding(12)
        }
        .onAppear { inputFocused = true }
        .onDisappear { streamTask?.cancel() }
    }

    private var subtitle: String {
        if appState.watchedFolderPaths.isEmpty {
            return "Add a watched folder in Settings so Arca has documents to read."
        }
        return appState.indexStatus?.summary ?? "Answers come only from the documents in your watched folders."
    }

    private var emptyState: some View {
        VStack(spacing: 16) {
            Image(systemName: "doc.text.magnifyingglass")
                .font(.system(size: 44))
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text("Ask a question about your documents")
                .font(.title3)
            Text("Arca searches PDFs, Word documents, Markdown and text files in your watched folders, then answers with the local AI and shows which files it used.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .frame(maxWidth: 440)
            VStack(spacing: 8) {
                ForEach(examples, id: \.self) { example in
                    Button(example) {
                        question = example
                        inputFocused = true
                    }
                    .buttonStyle(.bordered)
                }
            }
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func send() {
        let q = question.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty, !isStreaming else { return }
        question = ""
        messages.append(ChatMessage(text: q, role: .user))
        let answer = ChatMessage(text: "", role: .assistant)
        messages.append(answer)
        streamTask = Task { await stream(question: q, into: answer.id) }
    }

    private func stream(question: String, into id: UUID) async {
        defer { streamTask = nil }
        func update(_ change: (inout ChatMessage) -> Void) {
            if let idx = messages.firstIndex(where: { $0.id == id }) { change(&messages[idx]) }
        }
        update { $0.isThinking = true }
        do {
            for try await event in APIClient.shared.chat(question: question) {
                switch event {
                case .sources(let sources):
                    update { $0.sources = sources }
                case .token(let token):
                    update { $0.isThinking = false; $0.text += token }
                case .error(let message):
                    update { $0.error = message }
                case .done:
                    break
                }
            }
            if Task.isCancelled { update { $0.stopped = true } }
        } catch {
            if Task.isCancelled {
                update { $0.stopped = true }
            } else {
                update { $0.error = error.localizedDescription }
            }
        }
        update { $0.isThinking = false }
    }
}

struct ChatMessage: Identifiable {
    let id = UUID()
    var text: String
    let role: Role
    var sources: [ChatSource] = []
    var error: String?
    var isThinking = false
    var stopped = false

    enum Role { case user, assistant }
}

struct ChatBubble: View {
    let message: ChatMessage

    var body: some View {
        HStack(alignment: .top) {
            if message.role == .user { Spacer(minLength: 80) }
            VStack(alignment: message.role == .user ? .trailing : .leading, spacing: 8) {
                bubble
                if !message.sources.isEmpty {
                    SourcesView(sources: message.sources)
                }
            }
            if message.role == .assistant { Spacer(minLength: 80) }
        }
    }

    @ViewBuilder
    private var bubble: some View {
        if message.role == .user {
            Text(message.text)
                .textSelection(.enabled)
                .padding(10)
                .background(Color.accentColor)
                .foregroundStyle(.white)
                .clipShape(RoundedRectangle(cornerRadius: 12))
        } else {
            VStack(alignment: .leading, spacing: 6) {
                if message.isThinking && message.text.isEmpty {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text(message.sources.isEmpty ? "Searching your documents…" : "Thinking…")
                            .foregroundStyle(.secondary)
                    }
                }
                if !message.text.isEmpty {
                    Text(Self.markdown(message.text))
                        .textSelection(.enabled)
                }
                if let error = message.error {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if message.stopped {
                    Text("Stopped").font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding(10)
            .background(Color(nsColor: .controlBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: 12))
        }
    }
}

extension ChatBubble {
    /// Renders inline markdown (bold, lists as text) from the model; falls
    /// back to plain text when the partial answer is not valid markdown.
    static func markdown(_ text: String) -> AttributedString {
        let options = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        return (try? AttributedString(markdown: text, options: options)) ?? AttributedString(text)
    }
}

/// The files an answer drew on; click to open, right-click to reveal.
private struct SourcesView: View {
    let sources: [ChatSource]

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Sources").font(.caption).foregroundStyle(.secondary)
            FlowRow(sources: sources)
        }
    }

    private struct FlowRow: View {
        let sources: [ChatSource]

        var body: some View {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) { chips }
                VStack(alignment: .leading, spacing: 4) { chips }
            }
        }

        @ViewBuilder
        private var chips: some View {
            ForEach(sources, id: \.self) { source in
                let url = URL(fileURLWithPath: source.path)
                Button {
                    NSWorkspace.shared.open(url)
                } label: {
                    Label(source.name, systemImage: "doc")
                        .font(.caption)
                        .lineLimit(1)
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .help(source.path)
                .contextMenu {
                    Button("Open") { NSWorkspace.shared.open(url) }
                    Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([url]) }
                }
            }
        }
    }
}
