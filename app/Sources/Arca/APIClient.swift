import Foundation

// All HTTP communication with the Go backend goes through this type.
// No view may call URLSession directly.
actor APIClient {
    static let shared = APIClient()

    private let base = URL(string: "http://localhost:44742")!
    private let session: URLSession
    private var bearerToken: String?

    func setBearerToken(_ token: String) { bearerToken = token }

    private func authenticated(_ request: URLRequest) -> URLRequest {
        var request = request
        if let bearerToken { request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization") }
        return request
    }

    private init() {
        let cfg = URLSessionConfiguration.default
        // Local LLM calls can take a while, especially the first one after
        // launch while the model loads. This is the idle timeout between
        // bytes, not a cap on the whole request.
        cfg.timeoutIntervalForRequest = 300
        cfg.timeoutIntervalForResource = 60 * 60 * 2 // model downloads
        cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
        session = URLSession(configuration: cfg)
    }

    // MARK: - Health

    /// True when the backend answers GET /health within a second.
    func isHealthy() async -> Bool {
        var req = URLRequest(url: base.appending(path: "/health"))
        req.timeoutInterval = 1
        guard let (_, response) = try? await session.data(for: authenticated(req)) else { return false }
        return (response as? HTTPURLResponse)?.statusCode == 200
    }

    // MARK: - Settings and indexing

    func settings() async throws -> ServerSettings {
        try await send(ServerSettings.self, request(path: "/api/v1/settings", method: "GET"))
    }

    /// Saves the watched folders. The server applies them after a restart.
    func setWatchedFolders(_ folders: [String]) async throws -> FoldersUpdate {
        var req = request(path: "/api/v1/settings/folders", method: "PUT")
        req.httpBody = try JSONEncoder().encode(["folders": folders])
        return try await send(FoldersUpdate.self, req)
    }

    func indexStatus() async throws -> IndexStatus {
        try await send(IndexStatus.self, request(path: "/api/v1/index/status", method: "GET"))
    }

    func rescan() async throws {
        _ = try await send([String: Bool].self, request(path: "/api/v1/index/rescan", method: "POST"))
    }

    // MARK: - Index

    func index(path: String) async throws -> IndexResult {
        var req = request(path: "/api/v1/index", method: "POST")
        req.httpBody = try JSONEncoder().encode(["path": path])
        return try await send(IndexResult.self, req)
    }

    // MARK: - Sort

    func sort(folder: String) async throws -> SortResult {
        var req = request(path: "/api/v1/sort", method: "POST")
        req.httpBody = try JSONEncoder().encode(["folder": folder])
        return try await send(SortResult.self, req)
    }

    func applySort(folder: String, moves: [SortMove]) async throws -> SortApplyResult {
        struct Body: Encodable { let folder: String; let moves: [SortMove] }
        var req = request(path: "/api/v1/sort/apply", method: "POST")
        req.httpBody = try JSONEncoder().encode(Body(folder: folder, moves: moves))
        return try await send(SortApplyResult.self, req)
    }

    // MARK: - Decisions (docs/api/decision-pipeline.md)

    func decisions(status: DecisionStatus? = nil, limit: Int = 50) async throws -> DecisionPage {
        var items = [URLQueryItem(name: "limit", value: String(limit))]
        if let status { items.append(URLQueryItem(name: "status", value: status.rawValue)) }
        let url = base.appending(path: "/api/v1/decisions").appending(queryItems: items)
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        return try await send(DecisionPage.self, req)
    }

    /// Accepts a decision. `target`, relative to the decision's folder,
    /// overrides the suggested destination.
    func acceptDecision(id: Int64, target: String? = nil) async throws -> Decision {
        var req = request(path: "/api/v1/decisions/\(id)/accept", method: "POST")
        if let target { req.httpBody = try JSONEncoder().encode(["target": target]) }
        return try await send(Decision.self, req)
    }

    func rejectDecision(id: Int64) async throws -> Decision {
        try await send(Decision.self, request(path: "/api/v1/decisions/\(id)/reject", method: "POST"))
    }

    func undo(journalID: Int64) async throws {
        struct Undone: Decodable { let undone: Bool }
        _ = try await send(Undone.self, request(path: "/api/v1/journal/\(journalID)/undo", method: "POST"))
    }

    func folderPolicy(root: String) async throws -> FolderPolicy {
        try await send(FolderPolicy.self, request(path: "/api/v1/folders/\(Self.folderID(root))/policy", method: "GET"))
    }

    func setFolderPolicy(root: String, mode: FolderMode) async throws -> FolderPolicy {
        var req = request(path: "/api/v1/folders/\(Self.folderID(root))/policy", method: "PUT")
        req.httpBody = try JSONEncoder().encode(["mode": mode.rawValue])
        return try await send(FolderPolicy.self, req)
    }

    /// The API addresses a folder by its path, base64url-encoded without padding.
    static func folderID(_ root: String) -> String {
        Data(root.utf8).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    func calibration() async throws -> CalibrationPage {
        try await send(CalibrationPage.self, request(path: "/api/v1/calibration", method: "GET"))
    }

    func prepareCalibration(_ item: CalibrationItem) async throws -> Decision {
        var req = request(path: "/api/v1/calibration/decisions", method: "POST")
        req.httpBody = try JSONEncoder().encode(["root": item.root, "file": item.file])
        return try await send(Decision.self, req)
    }

    // MARK: - Tables

    func tables() async throws -> [TableInfo] {
        try await send([TableInfo].self, request(path: "/api/v1/tables", method: "GET"))
    }

    func importTable(path: String) async throws -> [TableInfo] {
        var req = request(path: "/api/v1/tables/import", method: "POST")
        req.httpBody = try JSONEncoder().encode(["path": path])
        return try await send([TableInfo].self, req)
    }

    func dropTable(name: String) async throws {
        var req = URLRequest(url: base.appending(path: "/api/v1/tables").appending(component: name))
        req.httpMethod = "DELETE"
        _ = try await send([String: String].self, req)
    }

    // MARK: - Rules (docs/api/rules.md)

    func listRules() async throws -> RuleList {
        try await rulesCall(RuleList.self, url: rulesBase, method: "GET")
    }

    func createRule(_ draft: RuleDraft) async throws -> RuleSaved {
        try await rulesCall(RuleSaved.self, url: rulesBase, method: "POST", body: JSONEncoder().encode(draft))
    }

    func updateRule(id: String, draft: RuleDraft) async throws -> RuleSaved {
        try await rulesCall(RuleSaved.self, url: rulesBase.appending(component: id), method: "PUT", body: JSONEncoder().encode(draft))
    }

    func deleteRule(id: String) async throws -> RuleDeleted {
        try await rulesCall(RuleDeleted.self, url: rulesBase.appending(component: id), method: "DELETE")
    }

    func setRuleOrder(_ ids: [String]) async throws -> RuleOrderResult {
        try await rulesCall(RuleOrderResult.self, url: rulesBase.appending(component: "order"), method: "PUT", body: JSONEncoder().encode(RuleOrderRequest(order: ids)))
    }

    func validateRule(_ draft: RuleDraft) async throws -> RuleValidation {
        try await rulesCall(RuleValidation.self, url: rulesBase.appending(component: "validate"), method: "POST", body: JSONEncoder().encode(draft))
    }

    func testRule(_ draft: RuleDraft, facts: RuleFacts) async throws -> RuleTestResult {
        let body = try JSONEncoder().encode(RuleTestRequest(rule: draft, facts: facts))
        return try await rulesCall(RuleTestResult.self, url: rulesBase.appending(component: "test"), method: "POST", body: body)
    }

    func ruleSuggestions() async throws -> RuleSuggestionList {
        try await rulesCall(RuleSuggestionList.self, url: rulesBase.appending(component: "suggestions"), method: "GET")
    }

    private var rulesBase: URL { base.appending(path: "/api/v1/rules") }

    // The rules API reports field-level problems as 409/422 with data.issues, so
    // these calls decode by HTTP status (see RulesResponseDecoder) instead of
    // through `decode(_:from:)`.
    private func rulesCall<T: Decodable>(_ type: T.Type, url: URL, method: String, body: Data? = nil) async throws -> T {
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = body
        let (data, response) = try await session.data(for: authenticated(req))
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        return try RulesResponseDecoder.decode(type, from: data, status: status)
    }

    // MARK: - Chat (SSE)

    /// Streams the answer to a question about the indexed documents.
    /// Cancelling the consuming task closes the connection.
    nonisolated func chat(question: String) -> AsyncThrowingStream<ChatEvent, Error> {
        let url = base.appending(path: "/api/v1/chat").appending(queryItems: [.init(name: "q", value: question)])
        return sse(url: url) { json in
            guard let event = ChatEvent.parse(json) else { return (nil, false) }
            if case .done = event { return (event, true) }
            if case .error = event { return (event, true) }
            return (event, false)
        }
    }

    // MARK: - Query

    func query(question: String, schemaHint: String = "") async throws -> QueryResult {
        var req = request(path: "/api/v1/query", method: "POST")
        req.httpBody = try JSONEncoder().encode(["question": question, "schema_hint": schemaHint])
        return try await send(QueryResult.self, req)
    }

    // MARK: - Models

    func models() async throws -> [ModelStatus] {
        try await send([ModelStatus].self, request(path: "/api/v1/models", method: "GET"))
    }

    func activateModel(id: String) async throws {
        var req = request(path: "/api/v1/models/activate", method: "POST")
        req.httpBody = try JSONEncoder().encode(["id": id])
        _ = try await send([String: String].self, req)
    }

    // MARK: - Model Download (SSE)

    nonisolated func downloadModel(id: String) -> AsyncThrowingStream<DownloadProgress, Error> {
        let url = base.appending(path: "/api/v1/models/download").appending(queryItems: [.init(name: "id", value: id)])
        return sse(url: url) { json in
            guard let data = json.data(using: .utf8),
                  let progress = try? JSONDecoder().decode(DownloadProgress.self, from: data)
            else { return (nil, false) }
            if let err = progress.error, !err.isEmpty {
                return (progress, true)
            }
            return (progress, progress.done)
        }
    }

    // MARK: - Helpers

    /// Reads `data:` lines from a server-sent event stream. `parse` turns one
    /// JSON payload into a value to yield (or nil to skip) and says whether
    /// the stream is complete. Network errors end the stream with the error.
    nonisolated private func sse<T: Sendable>(url: URL, parse: @escaping @Sendable (String) -> (T?, Bool)) -> AsyncThrowingStream<T, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let request = await authenticated(URLRequest(url: url))
                    let (bytes, response) = try await session.bytes(for: request)
                    if let http = response as? HTTPURLResponse, http.statusCode >= 400 {
                        throw APIError.http(http.statusCode)
                    }
                    for try await line in bytes.lines {
                        guard line.hasPrefix("data: ") else { continue }
                        let (value, finished) = parse(String(line.dropFirst(6)))
                        if let value { continuation.yield(value) }
                        if finished { break }
                    }
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: APIError.wrap(error))
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func request(path: String, method: String) -> URLRequest {
        var r = URLRequest(url: base.appending(path: path))
        r.httpMethod = method
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        return r
    }

    private func send<T: Decodable>(_ type: T.Type, _ req: URLRequest) async throws -> T {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: authenticated(req))
        } catch {
            throw APIError.wrap(error)
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        return try decode(type, from: data, status: status)
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data, status: Int) throws -> T {
        guard let envelope = try? JSONDecoder().decode(APIEnvelope<T>.self, from: data) else {
            // A non-envelope body: either an error page or a decoding bug.
            if status >= 400 { throw APIError.http(status) }
            _ = try JSONDecoder().decode(APIEnvelope<T>.self, from: data) // rethrow the real error
            throw APIError.missingData
        }
        if let err = envelope.error, !err.isEmpty {
            throw APIError.serverError(err)
        }
        guard let payload = envelope.data else {
            throw APIError.missingData
        }
        return payload
    }
}

// MARK: - Response types

// Mirrors the backend envelope: {"data": ..., "error": null, "meta": {"timestamp": "<RFC3339>"}}.
// `error` is null on success and a message string on failure; `data` is null on failure.
private struct APIEnvelope<T: Decodable>: Decodable {
    let data: T?
    let error: String?
    let meta: APIMeta?
}

private struct APIMeta: Decodable {
    let timestamp: String?
}

enum APIError: LocalizedError, Equatable {
    case serverError(String)
    case missingData
    case http(Int)
    case unreachable

    var errorDescription: String? {
        switch self {
        case .serverError(let msg): return msg
        case .missingData: return "The Arca server sent an empty response."
        case .http(429): return "Too many requests at once. Try again in a moment."
        case .http(let code): return "The Arca server answered with HTTP \(code)."
        case .unreachable: return "Arca's background server is not running. It restarts automatically; if this persists, use Settings → General → Restart Server."
        }
    }

    /// Maps connection failures to `.unreachable` and keeps everything else.
    static func wrap(_ error: Error) -> Error {
        if let urlError = error as? URLError,
           [.cannotConnectToHost, .networkConnectionLost, .cannotFindHost, .notConnectedToInternet].contains(urlError.code) {
            return APIError.unreachable
        }
        return error
    }
}

struct ServerSettings: Decodable, Equatable {
    let version: String
    let watchedFolders: [String]
    let activeModel: String
    let embeddingReady: Bool
    let dataDir: String
    let logFile: String

    enum CodingKeys: String, CodingKey {
        case version
        case watchedFolders = "watched_folders"
        case activeModel = "active_model"
        case embeddingReady = "embedding_ready"
        case dataDir = "data_dir"
        case logFile = "log_file"
    }

    /// Ready to answer questions: an LLM is active and search works.
    var modelsReady: Bool { !activeModel.isEmpty && embeddingReady }
}

struct FoldersUpdate: Decodable {
    let watchedFolders: [String]
    let restartRequired: Bool

    enum CodingKeys: String, CodingKey {
        case watchedFolders = "watched_folders"
        case restartRequired = "restart_required"
    }
}

struct IndexStatus: Decodable, Equatable {
    let running: Bool
    let found: Int
    let done: Int
    let indexed: Int
    let unchanged: Int
    let failed: Int
    let removed: Int
    let chunks: Int
    let stopped: String?
    let finishedAt: String?

    enum CodingKeys: String, CodingKey {
        case running, found, done, indexed, unchanged, failed, removed, chunks, stopped
        case finishedAt = "finished_at"
    }

    /// Files that are searchable after this scan.
    var searchable: Int { indexed + unchanged }

    var summary: String {
        if running {
            return found == 0 ? "Looking for documents…" : "Reading documents: \(done) of \(found)"
        }
        if let stopped, !stopped.isEmpty {
            return "Indexing paused: \(stopped)"
        }
        if found == 0 { return "No documents found in the watched folders yet" }
        var text = "\(searchable) document\(searchable == 1 ? "" : "s") searchable"
        if failed > 0 { text += " · \(failed) could not be read" }
        return text
    }
}

struct IndexResult: Decodable {
    let indexed: Int
}

struct SortMove: Codable, Hashable {
    let file: String
    let suggestedFolder: String
    enum CodingKeys: String, CodingKey {
        case file
        case suggestedFolder = "suggested_folder"
    }
}

struct SortResult: Decodable {
    let moves: [SortMove]
}

struct SortApplyResult: Decodable {
    let applied: Int
    let skipped: Int
    let errors: [String]?
}

enum DecisionStatus: String, Codable {
    case pending, accepted, rejected, applied, failed
}

struct Decision: Decodable, Identifiable, Equatable {
    let id: Int64
    let ts: String
    let root: String
    let file: String
    let action: String
    let target: String
    let confidence: Double
    let reason: String
    let source: String
    let status: DecisionStatus
    let journalID: Int64?

    enum CodingKeys: String, CodingKey {
        case id, ts, root, file, action, target, confidence, reason, source, status
        case journalID = "journal_id"
    }

    var fileName: String { (file as NSString).lastPathComponent }
    var folderName: String { (root as NSString).lastPathComponent }
    var fileURL: URL { URL(fileURLWithPath: root).appendingPathComponent(file) }

    var date: Date? {
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return withFraction.date(from: ts) ?? ISO8601DateFormatter().date(from: ts)
    }

    /// Who suggested it, in words.
    var sourceDescription: String {
        switch source {
        case "rules": return "your rule"
        case "semantic": return "similar files"
        case "llm": return "the local AI"
        case "cluster": return "Tidy a Folder"
        case "user": return "you"
        default: return source
        }
    }
}

struct DecisionPage: Decodable {
    let items: [Decision]
    let total: Int
}

enum FolderMode: String, Codable, CaseIterable, Identifiable {
    case off, suggest, auto
    var id: String { rawValue }

    var title: String {
        switch self {
        case .off: return "Off"
        case .suggest: return "Suggest"
        case .auto: return "Automatic"
        }
    }
}

struct FolderPolicy: Decodable, Equatable {
    let root: String
    let mode: FolderMode
    let autoEligible: Bool

    enum CodingKeys: String, CodingKey {
        case root, mode
        case autoEligible = "auto_eligible"
    }
}

struct TableColumnInfo: Decodable, Hashable {
    let name: String
    let type: String
}

struct TableInfo: Decodable, Identifiable, Hashable {
    let name: String
    let source: String
    let rows: Int
    let columns: [TableColumnInfo]
    var id: String { name }
}

struct QueryResult: Decodable {
    let columns: [String]
    let rows: [[AnyCodable]]
    let sql: String?
    let truncated: Bool?

    enum CodingKeys: String, CodingKey {
        case columns, rows, sql, truncated
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        columns = try c.decodeIfPresent([String].self, forKey: .columns) ?? []
        rows = try c.decodeIfPresent([[AnyCodable]].self, forKey: .rows) ?? []
        sql = try c.decodeIfPresent(String.self, forKey: .sql)
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated)
    }
}

struct ModelStatus: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let sizeGb: Double
    let downloaded: Bool
    let kind: String
    let description: String
    let active: Bool

    enum CodingKeys: String, CodingKey {
        case id, name, downloaded, kind, description, active
        case sizeGb = "size_gb"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        sizeGb = try c.decode(Double.self, forKey: .sizeGb)
        downloaded = try c.decode(Bool.self, forKey: .downloaded)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "llm"
        description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
        active = try c.decodeIfPresent(Bool.self, forKey: .active) ?? false
    }

    var isLanguageModel: Bool { kind == "llm" }
    static let embeddingID = "nomic-embed"
}

struct DownloadProgress: Decodable, Sendable {
    let modelId: String
    let downloaded: Int64
    let total: Int64
    let percent: Double
    let done: Bool
    let error: String?

    enum CodingKeys: String, CodingKey {
        case modelId = "model_id"
        case downloaded, total, percent, done, error
    }
}

struct ChatSource: Decodable, Hashable, Sendable {
    let path: String
    let name: String
}

/// One server-sent event of GET /api/v1/chat.
enum ChatEvent: Equatable, Sendable {
    case sources([ChatSource])
    case token(String)
    case error(String)
    case done

    static func parse(_ json: String) -> ChatEvent? {
        struct Wire: Decodable {
            let sources: [ChatSource]?
            let token: String?
            let error: String?
            let done: Bool?
        }
        guard let data = json.data(using: .utf8),
              let wire = try? JSONDecoder().decode(Wire.self, from: data)
        else { return nil }
        if let error = wire.error { return .error(error) }
        if let sources = wire.sources { return .sources(sources) }
        if let token = wire.token { return .token(token) }
        if wire.done == true { return .done }
        return nil
    }
}

// Minimal Codable wrapper for heterogeneous JSON values in query rows.
struct AnyCodable: Codable {
    let value: Any?

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { value = nil }
        else if let v = try? c.decode(Bool.self) { value = v }
        else if let v = try? c.decode(Int.self) { value = v }
        else if let v = try? c.decode(Double.self) { value = v }
        else if let v = try? c.decode(String.self) { value = v }
        else { value = nil }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch value {
        case let v as String: try c.encode(v)
        case let v as Double: try c.encode(v)
        case let v as Int: try c.encode(v)
        case let v as Bool: try c.encode(v)
        default: try c.encodeNil()
        }
    }

    /// The value as shown in a table cell.
    var display: String {
        switch value {
        case nil: return ""
        case let v as Double:
            return v.rounded() == v && abs(v) < 1e15 ? String(Int64(v)) : String(format: "%.2f", v)
        case let v?: return "\(v)"
        }
    }
}

struct CalibrationPage: Decodable { let items: [CalibrationItem] }
struct CalibrationItem: Decodable, Identifiable {
    let root: String
    let file: String
    let candidates: [CalibrationCandidate]
    let margin: Double
    var id: String { root + "/" + file }
}
struct CalibrationCandidate: Decodable, Identifiable {
    let target: String
    let score: Double
    var id: String { target }
}
