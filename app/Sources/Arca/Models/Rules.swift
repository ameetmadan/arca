import Foundation
import UniformTypeIdentifiers

// Typed models for the rules API (/api/v1/rules). The JSON contract is
// documented in docs/api/rules.md; keys below must match it exactly.
// This file holds no UI and makes no HTTP calls (APIClient does that).

// MARK: - Actions

enum RuleAction: String, CaseIterable, Identifiable {
    case move
    case tag
    case index
    case ignore
    case askUser = "ask_user"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .move: return "Move to folder"
        case .tag: return "Add tag"
        case .index: return "Index only"
        case .ignore: return "Ignore"
        case .askUser: return "Ask me"
        }
    }

    /// Only `move` (folder) and `tag` (the tag) take a target; the server
    /// rejects a target for the other actions.
    var usesTarget: Bool { self == .move || self == .tag }

    var targetLabel: String {
        switch self {
        case .move: return "Target folder"
        case .tag: return "Tag"
        default: return "Target"
        }
    }

    var targetPlaceholder: String {
        switch self {
        case .move: return "Finance/Invoices (relative to the watched folder)"
        case .tag: return "invoice"
        default: return "Not used for this action"
        }
    }

    /// Human-readable title for an action string from the server, which may be
    /// one this app version does not know.
    static func title(forRaw raw: String) -> String {
        RuleAction(rawValue: raw)?.title ?? raw
    }
}

// MARK: - Responses

/// One field-level problem (`data.issues[]` and `Rule.issue`).
struct RuleIssue: Decodable, Equatable, Identifiable {
    let field: String
    let message: String
    let index: Int?
    let ruleID: String?

    var id: String { "\(field)|\(ruleID ?? "")|\(message)" }

    enum CodingKeys: String, CodingKey {
        case field, message, index
        case ruleID = "rule_id"
    }
}

/// A stored rule as returned by the server.
struct Rule: Decodable, Identifiable, Equatable {
    let id: String
    let when: String
    let action: String
    let target: String
    let reason: String
    /// False if the stored rule does not compile (only after hand-editing the file).
    let valid: Bool
    let issue: RuleIssue?
}

struct RuleLimits: Decodable, Equatable {
    let maxRules: Int
    let maxExpressionLength: Int
    let maxExpressionNodes: Int
    let maxIDLength: Int
    let maxTargetLength: Int
    let maxReasonLength: Int

    enum CodingKeys: String, CodingKey {
        case maxRules = "max_rules"
        case maxExpressionLength = "max_expression_length"
        case maxExpressionNodes = "max_expression_nodes"
        case maxIDLength = "max_id_length"
        case maxTargetLength = "max_target_length"
        case maxReasonLength = "max_reason_length"
    }

    /// Used until the first `GET /rules` answers; the server's values win.
    static let fallback = RuleLimits(
        maxRules: 1000, maxExpressionLength: 1000, maxExpressionNodes: 200,
        maxIDLength: 64, maxTargetLength: 512, maxReasonLength: 200
    )
}

struct RuleList: Decodable {
    let rules: [Rule]
    let limits: RuleLimits?
}

struct RuleSaved: Decodable {
    let rule: Rule
    let reloaded: Bool?
}

struct RuleDeleted: Decodable {
    let deleted: String
    let reloaded: Bool?
}

struct RuleOrderResult: Decodable {
    let rules: [Rule]
    let reloaded: Bool?
}

struct RuleValidation: Decodable, Equatable {
    let valid: Bool
    let issues: [RuleIssue]
}

struct RuleDecision: Decodable, Equatable {
    let action: String
    let target: String?
    let confidence: Double?
    let reason: String?
    let source: String?
}

struct RuleTestResult: Decodable, Equatable {
    let matched: Bool
    let reason: String?
    let decision: RuleDecision?
    let evaluationError: String?

    enum CodingKeys: String, CodingKey {
        case matched, reason, decision
        case evaluationError = "evaluation_error"
    }
}

/// The server's suggestions list has no defined item shape yet (it is always
/// empty). Items are decoded without looking at their content.
struct RuleSuggestion: Decodable, Equatable {
    init() {}
    init(from decoder: Decoder) throws {}
}

struct RuleSuggestionList: Decodable {
    let suggestions: [RuleSuggestion]
}

// MARK: - Requests

/// A rule as sent to the server. For `POST /rules/validate` every key is
/// optional; nil keys are left out of the JSON.
struct RuleDraft: Encodable, Equatable {
    var id: String?
    var when: String?
    var action: String?
    var target: String?
    var reason: String?
}

/// Facts about one file for `POST /rules/test`. Keys are snake case, `ext` is
/// lower case with the leading dot. `exif` and `vector` are not sent by this app.
struct RuleFacts: Encodable, Equatable {
    var path: String?
    var relPath: String?
    var ext: String?
    var mime: String?
    var size: Int64?
    var modified: String?
    var snippet: String?

    enum CodingKeys: String, CodingKey {
        case path
        case relPath = "rel_path"
        case ext, mime, size, modified, snippet
    }
}

struct RuleTestRequest: Encodable {
    let rule: RuleDraft
    let facts: RuleFacts
}

struct RuleOrderRequest: Encodable {
    let order: [String]
}

// MARK: - Errors and envelope decoding

enum RulesAPIError: LocalizedError, Equatable {
    /// 409/422: the server explained what is wrong, field by field.
    case issues(message: String, issues: [RuleIssue], status: Int)
    /// Any other failure that carried an `error` string.
    case server(message: String, status: Int)
    /// The body was not the expected envelope.
    case malformedResponse(status: Int)

    var errorDescription: String? {
        switch self {
        case .issues(let message, let issues, _):
            let joined = issues.map { $0.message }.joined(separator: " ")
            return joined.isEmpty ? message : joined
        case .server(let message, _):
            return message
        case .malformedResponse(let status):
            return status == 0 ? "Unexpected response from the Arca backend."
                : "Unexpected response from the Arca backend (HTTP \(status))."
        }
    }
}

private struct RulesEnvelope<T: Decodable>: Decodable {
    let data: T?
    let error: String?
}

private struct IssuesPayload: Decodable {
    let issues: [RuleIssue]?
}

/// Decodes the `{data, error, meta}` envelope for the rules endpoints.
/// Unlike the generic decoder in APIClient it looks at the HTTP status, because
/// 409/422 carry `data.issues` next to a non-empty `error`.
enum RulesResponseDecoder {
    static func decode<T: Decodable>(_ type: T.Type, from data: Data, status: Int) throws -> T {
        let decoder = JSONDecoder()

        if (200..<300).contains(status) {
            guard let envelope = try? decoder.decode(RulesEnvelope<T>.self, from: data) else {
                throw RulesAPIError.malformedResponse(status: status)
            }
            if let message = envelope.error, !message.isEmpty {
                throw RulesAPIError.server(message: message, status: status)
            }
            guard let payload = envelope.data else {
                throw RulesAPIError.malformedResponse(status: status)
            }
            return payload
        }

        guard let failure = try? decoder.decode(RulesEnvelope<IssuesPayload>.self, from: data) else {
            throw RulesAPIError.malformedResponse(status: status)
        }
        var message = "Request failed (HTTP \(status))."
        if let text = failure.error, !text.isEmpty { message = text }
        if let issues = failure.data?.issues, !issues.isEmpty {
            throw RulesAPIError.issues(message: message, issues: issues, status: status)
        }
        throw RulesAPIError.server(message: message, status: status)
    }
}

// MARK: - Facts for "Test against a file"

enum RuleFactsError: LocalizedError {
    case notAFile

    var errorDescription: String? {
        switch self {
        case .notAFile: return "Choose a file, not a folder."
        }
    }
}

/// Builds `RuleFacts` from a file on disk. The pure helpers are unit-tested;
/// `build` touches the file system.
enum RuleFactsBuilder {
    /// How much of a text file is sent as `snippet`. 8 KiB is well under the
    /// server's 32 KiB limit even if every byte were replaced by U+FFFD (3 bytes).
    static let snippetByteLimit = 8 * 1024

    /// ".PDF" is what the file says, ".pdf" is what rules compare with.
    static func normalizedExtension(_ pathExtension: String) -> String? {
        guard !pathExtension.isEmpty else { return nil }
        return "." + pathExtension.lowercased()
    }

    /// Path relative to the first watched root that contains it; falls back to
    /// the file name when the file is outside every root.
    static func relativePath(of path: String, roots: [String]) -> String {
        let sortedRoots = roots.sorted { $0.count > $1.count }
        for root in sortedRoots {
            let prefix = root.hasSuffix("/") ? root : root + "/"
            if path.hasPrefix(prefix), path.count > prefix.count {
                return String(path.dropFirst(prefix.count))
            }
        }
        return URL(fileURLWithPath: path).lastPathComponent
    }

    static func rfc3339(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }

    static func build(for url: URL, roots: [URL]) throws -> RuleFacts {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }

        let values = try url.resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey, .contentModificationDateKey])
        guard values.isRegularFile == true else { throw RuleFactsError.notAFile }

        let path = url.standardizedFileURL.path
        let type = UTType(filenameExtension: url.pathExtension)

        var facts = RuleFacts()
        facts.path = path
        facts.relPath = relativePath(of: path, roots: roots.map { $0.standardizedFileURL.path })
        facts.ext = normalizedExtension(url.pathExtension)
        facts.mime = type?.preferredMIMEType
        if let size = values.fileSize { facts.size = Int64(size) }
        if let modified = values.contentModificationDate { facts.modified = rfc3339(modified) }
        // Only plain-text files get a snippet: this app does not extract text
        // from PDFs or images, so rules on Snippet cannot be tested against those here.
        if let type = type, type.conforms(to: .text) {
            facts.snippet = try readSnippet(from: url)
        }
        return facts
    }

    private static func readSnippet(from url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        let bytes = try handle.read(upToCount: snippetByteLimit) ?? Data()
        return String(decoding: bytes, as: UTF8.self)
    }
}
