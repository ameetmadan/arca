import XCTest
@testable import Arca

// Decoding tests for the backend's wire formats. The JSON mirrors what the Go
// server sends (see backend/cmd/server/*_test.go for the other side).
final class APIModelsTests: XCTestCase {
    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSONDecoder().decode(type, from: Data(json.utf8))
    }

    func testCalibrationCandidates() throws {
        let page = try decode(CalibrationPage.self, #"{"items":[{"root":"/r","file":"a.txt","margin":0.01,"candidates":[{"target":"Notes","score":0.8}]}]}"#)
        XCTAssertEqual(page.items[0].id, "/r/a.txt")
        XCTAssertEqual(page.items[0].candidates[0].target, "Notes")
        XCTAssertEqual(page.items[0].margin, 0.01)
    }

    func testChatEvents() {
        XCTAssertEqual(ChatEvent.parse(#"{"sources":[{"path":"/d/a.pdf","name":"a.pdf"}]}"#),
                       .sources([ChatSource(path: "/d/a.pdf", name: "a.pdf")]))
        XCTAssertEqual(ChatEvent.parse(#"{"token":"Hello"}"#), .token("Hello"))
        XCTAssertEqual(ChatEvent.parse(#"{"error":"no model"}"#), .error("no model"))
        XCTAssertEqual(ChatEvent.parse(#"{"done":true}"#), .done)
        XCTAssertNil(ChatEvent.parse("not json"))
    }

    func testServerSettings() throws {
        let s = try decode(ServerSettings.self, """
        {"version":"0.1.0","watched_folders":["/Users/a/Downloads"],"active_model":"qwen25-3b",
         "embedding_ready":true,"data_dir":"/x","log_file":"/y"}
        """)
        XCTAssertEqual(s.watchedFolders, ["/Users/a/Downloads"])
        XCTAssertTrue(s.modelsReady)
    }

    func testQueryResultWithNullRowsAndMixedValues() throws {
        let empty = try decode(QueryResult.self, #"{"columns":["a"],"rows":null,"sql":"SELECT 1","truncated":false}"#)
        XCTAssertEqual(empty.rows.count, 0)
        XCTAssertEqual(empty.sql, "SELECT 1")

        let r = try decode(QueryResult.self, #"{"columns":["n","x","s","b"],"rows":[[3, 218.75, "Migros", true],[null, 2.0, "", false]]}"#)
        XCTAssertEqual(r.rows[0].map(\.display), ["3", "218.75", "Migros", "true"])
        XCTAssertEqual(r.rows[1][0].display, "")
        XCTAssertEqual(r.rows[1][1].display, "2")
    }

    func testModelStatusToleratesOlderServers() throws {
        let models = try decode([ModelStatus].self, """
        [{"id":"llama32-3b","name":"Llama","size_gb":2.0,"downloaded":false},
         {"id":"nomic-embed","name":"Nomic","size_gb":0.08,"downloaded":true,"kind":"embedding","description":"d","active":false}]
        """)
        XCTAssertTrue(models[0].isLanguageModel)
        XCTAssertFalse(models[1].isLanguageModel)
    }

    func testDecisionDecodesAndDescribes() throws {
        let d = try decode(Decision.self, """
        {"id":1,"ts":"2026-10-14T22:18:20.947952056Z","root":"/r","file":"sub/b.txt","action":"move",
         "target":"Travel","confidence":0.6,"reason":"x","source":"llm","status":"pending","journal_id":null}
        """)
        XCTAssertEqual(d.fileName, "b.txt")
        XCTAssertEqual(d.folderName, "r")
        XCTAssertEqual(d.status, .pending)
        XCTAssertNil(d.journalID)
        XCTAssertNotNil(d.date)
        XCTAssertEqual(d.sourceDescription, "the local AI")
    }

    func testIndexStatusSummary() throws {
        let running = try decode(IndexStatus.self, #"{"running":true,"found":10,"done":3,"indexed":3,"unchanged":0,"failed":0,"removed":0,"chunks":9}"#)
        XCTAssertEqual(running.summary, "Reading documents: 3 of 10")
        let paused = try decode(IndexStatus.self, #"{"running":false,"found":1,"done":0,"indexed":0,"unchanged":0,"failed":0,"removed":0,"chunks":0,"stopped":"no model"}"#)
        XCTAssertEqual(paused.summary, "Indexing paused: no model")
    }

    func testFolderIDIsUnpaddedBase64URL() {
        // "/a?>" encodes to "L2E/Pg==" in standard base64.
        XCTAssertEqual(APIClient.folderID("/a?>"), "L2E_Pg")
    }
}
