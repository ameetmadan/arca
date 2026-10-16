import AppKit
import SwiftUI
import XCTest
@testable import Arca

/// Run on the macOS CI host with a real stub-provider Go server. No models or
/// user documents are needed. The normal app session and HTTP client are used.
final class MacAcceptanceTests: XCTestCase {
    @MainActor
    func testAuthenticatedHTTPStreamingAndQuit() async throws {
        guard ProcessInfo.processInfo.environment["ARCA_ACCEPTANCE_TESTS"] == "1" else {
            throw XCTSkip("Requires the CI-built app/arca-server")
        }
        let backend = BackendProcess()
        defer { backend.stopForTermination() }
        await backend.start()
        XCTAssertEqual(backend.state, .running)
        let pid = try XCTUnwrap(backend.processIdentifier)
        let settings = try await APIClient.shared.settings()
        XCTAssertFalse(settings.version.isEmpty)

        let (_, response) = try await URLSession.shared.data(from: URL(string: "http://localhost:44742/api/v1/settings")!)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 401)

        // Stub inference emits a real SSE error event after authentication.
        // An absent bearer header instead throws HTTP 401 before any event.
        var events: [ChatEvent] = []
        for try await event in APIClient.shared.chat(question: "acceptance test") { events.append(event) }
        XCTAssertEqual(events.count, 1)
        guard let event = events.first, case .error(let message) = event else {
            return XCTFail("Expected the model-unavailable SSE event")
        }
        XCTAssertTrue(message.contains("embedding model"))

        NotificationCenter.default.post(name: NSApplication.willTerminateNotification, object: NSApplication.shared)
        XCTAssertEqual(backend.state, .stopped)
        XCTAssertEqual(kill(pid, 0), -1, "The child must be reaped before app termination returns")
        XCTAssertEqual(errno, ESRCH)
        let healthy = await APIClient.shared.isHealthy()
        XCTAssertFalse(healthy)
    }

    @MainActor
    func testInboxScreenshotsAndCalibrationKeyboard() async throws {
        guard let output = ProcessInfo.processInfo.environment["ARCA_SCREENSHOT_DIR"] else {
            throw XCTSkip("Set ARCA_SCREENSHOT_DIR to capture native UI acceptance evidence")
        }
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.regular)
        let backend = BackendProcess()
        let state = AppState(backend: backend)
        let decisions = try JSONDecoder().decode([Decision].self, from: Data("""
        [{"id":1,"ts":"2026-10-16T12:00:00Z","root":"/Users/demo/Documents","file":"invoice.txt","action":"move","target":"Finance","confidence":0.96,"reason":"Closest to existing invoices","source":"semantic","status":"pending","journal_id":null},
         {"id":2,"ts":"2026-10-16T12:00:00Z","root":"/Users/demo/Documents","file":"notes.txt","action":"move","target":"Notes","confidence":1.0,"reason":"rule: notes","source":"rules","status":"accepted","journal_id":1}]
        """.utf8))
        for populated in [false, true] {
            let view = InboxView(pending: populated ? [decisions[0]] : [], recent: populated ? [decisions[1]] : [])
                .environmentObject(state).environmentObject(backend)
            let window = host(view, size: NSSize(width: 1100, height: 650))
            await settle()
            try snapshot(window, to: URL(fileURLWithPath: output).appendingPathComponent(populated ? "inbox-populated.png" : "inbox-empty.png"))
            window.close()
        }

        let items = try JSONDecoder().decode(CalibrationPage.self, from: Data("""
        {"items":[{"root":"/demo","file":"first.txt","margin":0.01,"candidates":[{"target":"Notes","score":0.8}]},
                  {"root":"/demo","file":"second.txt","margin":0.02,"candidates":[{"target":"Notes","score":0.9}]}]}
        """.utf8)).items
        var answers: [String] = []
        let view = CalibrationView(items: items) { item, target in answers.append(item.file + ":" + target) }
        let window = host(view, size: NSSize(width: 560, height: 440))
        defer { window.close() }
        await settle()
        XCTAssertTrue(press("s", in: window), "Skip must be reachable from the keyboard")
        await settle()
        XCTAssertTrue(answers.isEmpty, "Skipping must not submit an answer")
        XCTAssertTrue(press("1", in: window), "Candidate one must be reachable from the keyboard")
        await settle()
        XCTAssertEqual(answers, ["second.txt:Notes"])
        XCTAssertTrue(press("\u{1b}", in: window), "Done must be reachable with Escape")
        try snapshot(window, to: URL(fileURLWithPath: output).appendingPathComponent("calibration-complete.png"))
    }

    @MainActor
    private func host<V: View>(_ view: V, size: NSSize) -> NSWindow {
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentViewController = NSHostingController(rootView: view)
        window.setContentSize(size)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        return window
    }

    @MainActor
    private func settle() async { try? await Task.sleep(for: .milliseconds(500)) }

    @MainActor
    private func press(_ key: String, in window: NSWindow) -> Bool {
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0,
                                    windowNumber: window.windowNumber, context: nil, characters: key,
                                    charactersIgnoringModifiers: key, isARepeat: false, keyCode: 0)!
        return window.performKeyEquivalent(with: event)
    }

    @MainActor
    private func snapshot(_ window: NSWindow, to url: URL) throws {
        let view = try XCTUnwrap(window.contentView)
        view.layoutSubtreeIfNeeded()
        let image = try XCTUnwrap(view.bitmapImageRepForCachingDisplay(in: view.bounds))
        view.cacheDisplay(in: view.bounds, to: image)
        let png = try XCTUnwrap(image.representation(using: .png, properties: [:]))
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try png.write(to: url)
        XCTAssertGreaterThan(png.count, 5000, "Screenshot should contain the rendered view")
    }
}
