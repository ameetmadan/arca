import Foundation

// All HTTP communication with the Go backend goes through this type.
// No view may call URLSession directly.
actor APIClient {
    static let shared = APIClient()

    private let base = URL(string: "http://localhost:44742")!
    private let session: URLSession

    private init() {
        let cfg = URLSessionConfiguration.default
        cfg.timeoutIntervalForRequest = 30
        session = URLSession(configuration: cfg)
    }

    // MARK: - Index

    func index(path: String) async throws -> IndexResult {
        var req = request(path: "/api/v1/index", method: "POST")
        req.httpBody = try JSONEncoder().encode(["path": path])
        let (data, _) = try await session.data(for: req)
        return try decode(IndexResult.self, from: data)
    }

    // MARK: - Sort

    func sort(folder: String) async throws -> SortResult {
        var req = request(path: "/api/v1/sort", method: "POST")
        req.httpBody = try JSONEncoder().encode(["folder": folder])
        let (data, _) = try await session.data(for: req)
        return try decode(SortResult.self, from: data)
    }

    func applySort(folder: String, moves: [SortMove]) async throws -> SortApplyResult {
        struct Body: Encodable { let folder: String; let moves: [SortMove] }
        var req = request(path: "/api/v1/sort/apply", method: "POST")
        req.httpBody = try JSONEncoder().encode(Body(folder: folder, moves: moves))
        let (data, _) = try await session.data(for: req)
        return try decode(SortApplyResult.self, from: data)
    }

    // MARK: - Chat (SSE)

    nonisolated func chat(question: String) -> AsyncThrowingStream<String, Error> {
        AsyncThrowingStream { continuation in
            Task {
                let url = base.appending(path: "/api/v1/chat").appending(queryItems: [.init(name: "q", value: question)])
                let (bytes, _) = try await session.bytes(from: url)
                for try await line in bytes.lines {
                    if line.hasPrefix("data: ") {
                        let json = String(line.dropFirst(6))
                        if let token = parseSSEToken(json) {
                            continuation.yield(token)
                        }
                        if json.contains("\"done\":true") {
                            continuation.finish()
                            return
                        }
                    }
                }
                continuation.finish()
            }
        }
    }

    // MARK: - Query

    func query(question: String, schemaHint: String = "") async throws -> QueryResult {
        var req = request(path: "/api/v1/query", method: "POST")
        req.httpBody = try JSONEncoder().encode(["question": question, "schema_hint": schemaHint])
        let (data, _) = try await session.data(for: req)
        return try decode(QueryResult.self, from: data)
    }

    // MARK: - Models

    func models() async throws -> [ModelStatus] {
        let req = request(path: "/api/v1/models", method: "GET")
        let (data, _) = try await session.data(for: req)
        return try decode([ModelStatus].self, from: data)
    }

    func activateModel(id: String) async throws {
        var req = request(path: "/api/v1/models/activate", method: "POST")
        req.httpBody = try JSONEncoder().encode(["id": id])
        let (data, _) = try await session.data(for: req)
        _ = try decode([String: String].self, from: data)
    }

    // MARK: - Model Download (SSE)

    nonisolated func downloadModel(id: String) -> AsyncThrowingStream<DownloadProgress, Error> {
        AsyncThrowingStream { continuation in
            Task {
                let url = base.appending(path: "/api/v1/models/download").appending(queryItems: [.init(name: "id", value: id)])
                let (bytes, _) = try await session.bytes(from: url)
                for try await line in bytes.lines {
                    if line.hasPrefix("data: ") {
                        let json = String(line.dropFirst(6))
                        if let progress = parseDownloadProgress(json) {
                            continuation.yield(progress)
                            if progress.done { continuation.finish(); return }
                        }
                    }
                }
                continuation.finish()
            }
        }
    }

    // MARK: - Helpers

    private func request(path: String, method: String) -> URLRequest {
        var r = URLRequest(url: base.appending(path: path))
        r.httpMethod = method
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        return r
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        let envelope = try JSONDecoder().decode(APIEnvelope<T>.self, from: data)
        if let err = envelope.error, !err.isEmpty {
            throw APIError.serverError(err)
        }
        guard let payload = envelope.data else {
            throw APIError.missingData
        }
        return payload
    }

    nonisolated private func parseSSEToken(_ json: String) -> String? {
        guard let data = json.data(using: .utf8),
              let obj = try? JSONDecoder().decode([String: String].self, from: data)
        else { return nil }
        return obj["token"]
    }

    nonisolated private func parseDownloadProgress(_ json: String) -> DownloadProgress? {
        guard let data = json.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(DownloadProgress.self, from: data)
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

enum APIError: LocalizedError {
    case serverError(String)
    case missingData

    var errorDescription: String? {
        switch self {
        case .serverError(let msg): return msg
        case .missingData: return "No data in response"
        }
    }
}

struct IndexResult: Decodable {
    let indexed: Int
}

struct SortMove: Codable {
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

struct QueryResult: Decodable {
    let columns: [String]
    let rows: [[AnyCodable]]
}

struct ModelStatus: Decodable, Identifiable {
    let id: String
    let name: String
    let sizeGb: Double
    let downloaded: Bool

    enum CodingKeys: String, CodingKey {
        case id, name, downloaded
        case sizeGb = "size_gb"
    }
}

struct DownloadProgress: Decodable {
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

// Minimal Codable wrapper for heterogeneous JSON values in query rows.
struct AnyCodable: Codable {
    let value: Any?

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let v = try? c.decode(String.self) { value = v }
        else if let v = try? c.decode(Double.self) { value = v }
        else if let v = try? c.decode(Int.self) { value = v }
        else if let v = try? c.decode(Bool.self) { value = v }
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
}
