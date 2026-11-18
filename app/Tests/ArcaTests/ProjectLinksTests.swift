import XCTest
@testable import Arca

final class ProjectLinksTests: XCTestCase {
    func testFeedbackIssuePrefillsTheFormFields() throws {
        let url = ProjectLinks.feedbackIssue(arcaVersion: "0.1.0-beta.3 (42)", mac: "macOS 26.1.0, Apple M2, 16 GB")
        let items = try XCTUnwrap(URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems)
        let values = Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value) })

        XCTAssertEqual(url.host(), "github.com")
        XCTAssertEqual(values["template"], "beta-feedback.yml")
        XCTAssertEqual(values["version"], "0.1.0-beta.3 (42)")
        XCTAssertEqual(values["mac"], "macOS 26.1.0, Apple M2, 16 GB")
    }

    /// GitHub only prefills a field whose id matches the query name, and
    /// silently drops the rest, so a renamed field would go unnoticed.
    func testFeedbackFormHasTheFieldsTheAppFillsIn() throws {
        let form = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appending(path: "../../../.github/ISSUE_TEMPLATE/beta-feedback.yml")
        let yaml = try String(contentsOf: form, encoding: .utf8)

        XCTAssertTrue(yaml.contains("id: version"))
        XCTAssertTrue(yaml.contains("id: mac"))
    }
}
