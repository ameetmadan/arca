import SwiftUI

struct DocumentChatView: View {
    @State private var question: String = ""
    @State private var messages: [ChatMessage] = []
    @State private var isStreaming = false

    var body: some View {
        VStack(spacing: 0) {
            Text("Document Chat")
                .font(.title2)
                .fontWeight(.semibold)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()

            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 12) {
                        ForEach(messages) { msg in
                            ChatBubble(message: msg)
                        }
                    }
                    .padding()
                }
                .onChange(of: messages.count) { _, _ in
                    if let last = messages.last {
                        proxy.scrollTo(last.id, anchor: .bottom)
                    }
                }
            }

            Divider()

            HStack(spacing: 8) {
                TextField("Ask about your documents…", text: $question, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(1...4)
                    .onSubmit { Task { await sendQuestion() } }

                Button(action: { Task { await sendQuestion() } }) {
                    Image(systemName: isStreaming ? "stop.circle" : "arrow.up.circle.fill")
                        .font(.title2)
                }
                .disabled(question.isEmpty || isStreaming)
                .buttonStyle(.borderless)
            }
            .padding()
        }
        .frame(minWidth: 500, minHeight: 400)
    }

    private func sendQuestion() async {
        guard !question.isEmpty else { return }
        let q = question
        question = ""
        messages.append(ChatMessage(text: q, role: .user))
        let assistantMsg = ChatMessage(text: "", role: .assistant)
        messages.append(assistantMsg)
        isStreaming = true
        defer { isStreaming = false }

        do {
            for try await token in APIClient.shared.chat(question: q) {
                if let idx = messages.firstIndex(where: { $0.id == assistantMsg.id }) {
                    messages[idx].text += token
                }
            }
        } catch {
            if let idx = messages.firstIndex(where: { $0.id == assistantMsg.id }) {
                messages[idx].text = "Error: \(error.localizedDescription)"
            }
        }
    }
}

struct ChatMessage: Identifiable {
    let id = UUID()
    var text: String
    let role: Role

    enum Role { case user, assistant }
}

struct ChatBubble: View {
    let message: ChatMessage

    var body: some View {
        HStack {
            if message.role == .user { Spacer() }
            Text(message.text.isEmpty ? "…" : message.text)
                .padding(10)
                .background(message.role == .user ? Color.accentColor : Color(nsColor: .controlBackgroundColor))
                .foregroundStyle(message.role == .user ? .white : .primary)
                .clipShape(RoundedRectangle(cornerRadius: 12))
                .frame(maxWidth: 400, alignment: message.role == .user ? .trailing : .leading)
            if message.role == .assistant { Spacer() }
        }
    }
}
