import AppKit
import Darwin

/// Runs the Go backend (`arca-server`) as a child process and reports whether
/// it is usable.
///
/// The server is started with `-exit-with-parent`, so it also stops if the
/// app crashes. Its output goes to ~/Library/Logs/Arca/arca-server.log.
@MainActor
final class BackendProcess: ObservableObject {
    enum State: Equatable {
        case stopped
        case starting
        case running
        case failed(String)
    }

    @Published private(set) var state: State = .stopped

    private var process: Process?
    private var stopping = false
    /// Restarts after unexpected exits, reset once the server has run a while.
    private var crashRestarts = 0
    private var startedAt = Date.distantPast

    static let logURL: URL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Logs/Arca/arca-server.log")

    static let dataURL: URL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Application Support/Arca")

    var isRunning: Bool { state == .running }

    private var terminationObserver: NSObjectProtocol?

    init() {
        // Stop the server when the app quits. The observer runs on the main
        // queue, which is the main actor.
        terminationObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.stopForTermination() }
        }
    }

    func start() async {
        guard process == nil, state != .starting else { return }
        state = .starting
        stopping = false

        // An earlier server can still be shutting down (it holds the port and
        // the database locks). Give it a moment before starting another.
        if await APIClient.shared.isHealthy() {
            for _ in 0..<25 {
                try? await Task.sleep(for: .milliseconds(200))
                if !(await APIClient.shared.isHealthy()) { break }
            }
            if await APIClient.shared.isHealthy() {
                // Something else is serving the API (a server started by
                // hand, for example). Use it rather than fail.
                log("another Arca server is already running on port 44742; using it")
                state = .running
                return
            }
        }

        let binary = backendBinaryURL()
        guard FileManager.default.isExecutableFile(atPath: binary.path) else {
            state = .failed("Arca's background server is missing from the app (\(binary.path)). Download Arca again.")
            return
        }

        let p = Process()
        p.executableURL = binary
        p.arguments = ["-exit-with-parent"]
        if let handle = openLog() {
            p.standardOutput = handle
            p.standardError = handle
        } else {
            p.standardOutput = FileHandle.nullDevice
            p.standardError = FileHandle.nullDevice
        }
        p.terminationHandler = { [weak self] proc in
            let status = proc.terminationStatus
            Task { @MainActor in self?.processExited(proc, status: status) }
        }

        do {
            try p.run()
        } catch {
            state = .failed("Arca's background server could not be started: \(error.localizedDescription)")
            return
        }
        process = p
        startedAt = Date()

        // Wait for /health. Opening the databases takes well under a second;
        // allow more for a slow disk.
        for _ in 0..<150 {
            if await APIClient.shared.isHealthy() {
                state = .running
                return
            }
            guard process === p, p.isRunning else { return } // processExited reports it
            try? await Task.sleep(for: .milliseconds(200))
        }
        state = .failed("Arca's background server did not respond. The log may say why.")
    }

    /// Stops the server and waits for it to exit (it must release the
    /// database locks before a new one can start).
    func stop() async {
        stopping = true
        guard let p = process else {
            state = .stopped
            return
        }
        p.terminate() // SIGTERM: the server shuts down gracefully
        let deadline = Date().addingTimeInterval(12)
        while p.isRunning && Date() < deadline {
            try? await Task.sleep(for: .milliseconds(100))
        }
        if p.isRunning {
            kill(p.processIdentifier, SIGKILL)
        }
        process = nil
        state = .stopped
    }

    func restart() async {
        await stop()
        crashRestarts = 0
        await start()
    }

    /// Called when the app quits: there is no run loop left to await on, so
    /// this blocks briefly.
    func stopForTermination() {
        stopping = true
        guard let p = process, p.isRunning else { return }
        p.terminate()
        let deadline = Date().addingTimeInterval(5)
        while p.isRunning && Date() < deadline {
            usleep(50_000)
        }
        if p.isRunning { kill(p.processIdentifier, SIGKILL) }
    }

    private func processExited(_ p: Process, status: Int32) {
        guard p === process else { return }
        process = nil
        if stopping {
            state = .stopped
            return
        }
        log("server exited unexpectedly with status \(status)")
        // Restart automatically, but not in a loop if it keeps crashing.
        if Date().timeIntervalSince(startedAt) > 60 { crashRestarts = 0 }
        if crashRestarts < 2 {
            crashRestarts += 1
            state = .stopped
            Task { await start() }
        } else {
            state = .failed("Arca's background server keeps stopping (exit status \(status)). The log may say why.")
        }
    }

    private func backendBinaryURL() -> URL {
        // In the app bundle the binary sits in Contents/Resources.
        if let url = Bundle.main.url(forResource: "arca-server", withExtension: nil) {
            return url
        }
        // `swift run` during development: use `make bundle`'s copy in app/.
        return URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
            .appendingPathComponent("arca-server")
    }

    /// Opens the log for appending, rotating it once it passes 5 MB.
    private func openLog() -> FileHandle? {
        let fm = FileManager.default
        let url = Self.logURL
        try? fm.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let size = (try? fm.attributesOfItem(atPath: url.path))?[.size] as? Int, size > 5_000_000 {
            let old = url.appendingPathExtension("1")
            try? fm.removeItem(at: old)
            try? fm.moveItem(at: url, to: old)
        }
        if !fm.fileExists(atPath: url.path) {
            fm.createFile(atPath: url.path, contents: nil)
        }
        guard let handle = try? FileHandle(forWritingTo: url) else { return nil }
        _ = try? handle.seekToEnd()
        return handle
    }

    private func log(_ message: String) {
        guard let handle = openLog() else { return }
        let line = "\(ISO8601DateFormatter().string(from: Date())) [app] \(message)\n"
        try? handle.write(contentsOf: Data(line.utf8))
        try? handle.close()
    }
}
