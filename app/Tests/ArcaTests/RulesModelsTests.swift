import XCTest
@testable import Arca

// Decoding/encoding tests for the rules API models. The JSON below is copied
// from docs/api/rules.md, so a change in the contract shows up here.
final class RulesModelsTests: XCTestCase {
    private func data(_ json: String) -> Data { Data(json.utf8) }

    // MARK: Decoding

    func testListDecodesRulesAndLimits() throws {
        let json = """
        {
          "data": {
            "rules": [
              { "id": "invoices", "when": "Ext == \\".pdf\\"", "action": "move", "target": "Finance/Invoices",
                "reason": "", "valid": true, "issue": null },
              { "id": "broken", "when": "x", "action": "ignore", "target": "",
                "reason": "", "valid": false, "issue": { "field": "when", "message": "invalid expression" } }
            ],
            "limits": {
              "max_rules": 1000, "max_expression_length": 1000, "max_expression_nodes": 200,
              "max_id_length": 64, "max_target_length": 512, "max_reason_length": 200
            }
          },
          "error": null, "meta": { "timestamp": "2026-10-12T10:00:00Z" }
        }
        """
        let list = try RulesResponseDecoder.decode(RuleList.self, from: data(json), status: 200)
        XCTAssertEqual(list.rules.count, 2)
        XCTAssertEqual(list.rules[0].id, "invoices")
        XCTAssertEqual(list.rules[0].when, #"Ext == ".pdf""#)
        XCTAssertTrue(list.rules[0].valid)
        XCTAssertNil(list.rules[0].issue)
        XCTAssertFalse(list.rules[1].valid)
        XCTAssertEqual(list.rules[1].issue?.field, "when")
        XCTAssertEqual(list.limits, RuleLimits.fallback)
    }

    func testEmptyListIsNotNull() throws {
        let json = #"{ "data": { "rules": [] }, "error": null, "meta": { "timestamp": "t" } }"#
        let list = try RulesResponseDecoder.decode(RuleList.self, from: data(json), status: 200)
        XCTAssertTrue(list.rules.isEmpty)
        XCTAssertNil(list.limits)
    }

    func testSavedRuleDecodes() throws {
        let json = """
        { "data": { "rule": { "id": "a", "when": "true", "action": "tag", "target": "t", "reason": "r",
                              "valid": true, "issue": null }, "reloaded": false },
          "error": null, "meta": { "timestamp": "t" } }
        """
        let saved = try RulesResponseDecoder.decode(RuleSaved.self, from: data(json), status: 201)
        XCTAssertEqual(saved.rule.id, "a")
        XCTAssertEqual(saved.reloaded, false)
    }

    func testValidationDecodesIssues() throws {
        let json = """
        { "data": { "valid": false, "issues": [ { "field": "when", "message": "invalid expression: unexpected token EOF" } ] },
          "error": null, "meta": { "timestamp": "t" } }
        """
        let result = try RulesResponseDecoder.decode(RuleValidation.self, from: data(json), status: 200)
        XCTAssertFalse(result.valid)
        XCTAssertEqual(result.issues.first?.field, "when")
        XCTAssertNil(result.issues.first?.index)
    }

    func testTestResultMatch() throws {
        let json = """
        { "data": { "matched": true, "reason": "PDF mentioning an invoice",
                    "decision": { "action": "move", "target": "Finance/Invoices", "confidence": 1,
                                  "reason": "PDF mentioning an invoice", "source": "rule:preview" },
                    "evaluation_error": null },
          "error": null, "meta": { "timestamp": "t" } }
        """
        let result = try RulesResponseDecoder.decode(RuleTestResult.self, from: data(json), status: 200)
        XCTAssertTrue(result.matched)
        XCTAssertEqual(result.decision?.action, "move")
        XCTAssertEqual(result.decision?.confidence, 1)
        XCTAssertNil(result.evaluationError)
    }

    func testTestResultNoMatchAndEvaluationError() throws {
        let noMatch = """
        { "data": { "matched": false, "reason": "the rule did not match these facts",
                    "decision": null, "evaluation_error": null },
          "error": null, "meta": { "timestamp": "t" } }
        """
        let a = try RulesResponseDecoder.decode(RuleTestResult.self, from: data(noMatch), status: 200)
        XCTAssertFalse(a.matched)
        XCTAssertNil(a.decision)

        let failed = """
        { "data": { "matched": false, "reason": "the rule failed on these facts",
                    "decision": null, "evaluation_error": "evaluation failed" },
          "error": null, "meta": { "timestamp": "t" } }
        """
        let b = try RulesResponseDecoder.decode(RuleTestResult.self, from: data(failed), status: 200)
        XCTAssertEqual(b.evaluationError, "evaluation failed")
    }

    func testSuggestionsEmptyAndTypedItems() throws {
        let empty = #"{ "data": { "suggestions": [] }, "error": null, "meta": { "timestamp": "t" } }"#
        let a = try RulesResponseDecoder.decode(RuleSuggestionList.self, from: data(empty), status: 200)
        XCTAssertTrue(a.suggestions.isEmpty)

        let items = #"{ "data": { "suggestions": [ { "id": "correction-1", "root": "/r", "when": "Ext == \".txt\"", "action": "move", "target": "Notes", "reason": "3 corrections", "enabled": false, "count": 3 } ] }, "error": null, "meta": {} }"#
        let b = try RulesResponseDecoder.decode(RuleSuggestionList.self, from: data(items), status: 200)
        XCTAssertEqual(b.suggestions.count, 1)
        XCTAssertFalse(b.suggestions[0].enabled)
        XCTAssertEqual(b.suggestions[0].target, "Notes")
    }

    // MARK: Errors

    func test422CarriesIssues() {
        let json = """
        { "data": { "issues": [
            { "field": "when", "message": "invalid expression: unexpected token EOF", "index": 2, "rule_id": "invoices" } ] },
          "error": "rules are invalid: ...", "meta": { "timestamp": "t" } }
        """
        XCTAssertThrowsError(try RulesResponseDecoder.decode(RuleSaved.self, from: data(json), status: 422)) { error in
            guard case RulesAPIError.issues(let message, let issues, let status) = error else {
                return XCTFail("expected issues, got \(error)")
            }
            XCTAssertEqual(status, 422)
            XCTAssertEqual(message, "rules are invalid: ...")
            XCTAssertEqual(issues.count, 1)
            XCTAssertEqual(issues[0].index, 2)
            XCTAssertEqual(issues[0].ruleID, "invoices")
        }
    }

    func test409DuplicateIdCarriesIssues() {
        let json = #"{ "data": { "issues": [ { "field": "id", "message": "a rule with this id already exists" } ] }, "error": "conflict", "meta": {} }"#
        XCTAssertThrowsError(try RulesResponseDecoder.decode(RuleSaved.self, from: data(json), status: 409)) { error in
            guard case RulesAPIError.issues(_, let issues, let status) = error else {
                return XCTFail("expected issues, got \(error)")
            }
            XCTAssertEqual(status, 409)
            XCTAssertEqual(issues.first?.field, "id")
        }
    }

    func testNotFoundIsServerError() {
        let json = #"{ "data": null, "error": "rule not found", "meta": { "timestamp": "t" } }"#
        XCTAssertThrowsError(try RulesResponseDecoder.decode(RuleDeleted.self, from: data(json), status: 404)) { error in
            XCTAssertEqual(error as? RulesAPIError, .server(message: "rule not found", status: 404))
        }
    }

    func testNonJSONBodyIsMalformed() {
        XCTAssertThrowsError(try RulesResponseDecoder.decode(RuleList.self, from: data("Too Many Requests"), status: 429)) { error in
            XCTAssertEqual(error as? RulesAPIError, .malformedResponse(status: 429))
        }
    }

    func testSuccessWithErrorStringThrows() {
        let json = #"{ "data": null, "error": "boom", "meta": {} }"#
        XCTAssertThrowsError(try RulesResponseDecoder.decode(RuleList.self, from: data(json), status: 200))
    }

    // MARK: Encoding

    private func jsonObject<T: Encodable>(_ value: T) throws -> [String: Any] {
        let encoded = try JSONEncoder().encode(value)
        return try XCTUnwrap(try JSONSerialization.jsonObject(with: encoded) as? [String: Any])
    }

    func testValidationDraftOmitsNilKeys() throws {
        let object = try jsonObject(RuleDraft(when: #"Ext == ".pdf" &&"#))
        XCTAssertEqual(Set(object.keys), ["when"])
    }

    func testFullDraftKeys() throws {
        let draft = RuleDraft(id: "invoices", when: "true", action: "move", target: "Finance", reason: "why")
        let object = try jsonObject(draft)
        XCTAssertEqual(Set(object.keys), ["id", "when", "action", "target", "reason"])
    }

    func testFactsUseSnakeCaseKeys() throws {
        var facts = RuleFacts()
        facts.relPath = "scan.pdf"
        facts.ext = ".pdf"
        facts.size = 12
        facts.modified = "2026-01-02T03:04:05Z"
        let object = try jsonObject(facts)
        XCTAssertEqual(Set(object.keys), ["rel_path", "ext", "size", "modified"])
    }

    func testOrderRequestKey() throws {
        let object = try jsonObject(RuleOrderRequest(order: ["a", "b"]))
        XCTAssertEqual(object["order"] as? [String], ["a", "b"])
    }

    func testTestRequestShape() throws {
        let object = try jsonObject(RuleTestRequest(rule: RuleDraft(when: "true", action: "ignore"), facts: RuleFacts(ext: ".pdf")))
        XCTAssertEqual(Set(object.keys), ["rule", "facts"])
    }

    // MARK: Facts helpers

    func testExtensionIsLowerCaseWithDot() {
        XCTAssertEqual(RuleFactsBuilder.normalizedExtension("PDF"), ".pdf")
        XCTAssertEqual(RuleFactsBuilder.normalizedExtension("gz"), ".gz")
        XCTAssertNil(RuleFactsBuilder.normalizedExtension(""))
    }

    func testRelativePath() {
        let roots = ["/Users/me/Documents", "/Users/me/Documents/Work"]
        XCTAssertEqual(RuleFactsBuilder.relativePath(of: "/Users/me/Documents/Work/a.md", roots: roots), "a.md")
        XCTAssertEqual(RuleFactsBuilder.relativePath(of: "/Users/me/Documents/Invoices/a.pdf", roots: roots), "Invoices/a.pdf")
        XCTAssertEqual(RuleFactsBuilder.relativePath(of: "/tmp/other.txt", roots: roots), "other.txt")
        XCTAssertEqual(RuleFactsBuilder.relativePath(of: "/Users/me/DocumentsX/a.txt", roots: roots), "a.txt")
    }

    func testRFC3339() {
        let date = Date(timeIntervalSince1970: 1_767_323_045)
        XCTAssertEqual(RuleFactsBuilder.rfc3339(date), "2026-01-02T03:04:05Z")
    }

    // MARK: Actions

    func testActionTargetUse() {
        XCTAssertTrue(RuleAction.move.usesTarget)
        XCTAssertTrue(RuleAction.tag.usesTarget)
        XCTAssertFalse(RuleAction.index.usesTarget)
        XCTAssertFalse(RuleAction.ignore.usesTarget)
        XCTAssertFalse(RuleAction.askUser.usesTarget)
        XCTAssertEqual(RuleAction.askUser.rawValue, "ask_user")
    }
}
