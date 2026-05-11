import Foundation

// Manages the Go backend binary as a subprocess.
@MainActor
final class BackendProcess: ObservableObject {
    private var process: Process?

    func start() async {
        guard process == nil else { return }

        let binaryURL = backendBinaryURL()
        guard FileManager.default.isExecutableFile(atPath: binaryURL.path) else {
            print("BackendProcess: binary not found at \(binaryURL.path)")
            return
        }

        let p = Process()
        p.executableURL = binaryURL
        p.arguments = []
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.standardError

        do {
            try p.run()
            process = p
            print("BackendProcess: started (pid \(p.processIdentifier))")
        } catch {
            print("BackendProcess: failed to start: \(error)")
        }
    }

    func stop() {
        process?.terminate()
        process = nil
    }

    deinit {
        process?.terminate()
    }

    private func backendBinaryURL() -> URL {
        // In a packaged app the binary lives in the app bundle's Resources.
        if let bundleURL = Bundle.main.url(forResource: "arca-server", withExtension: nil) {
            return bundleURL
        }
        // During development fall back to the built binary next to the app.
        return URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
            .appendingPathComponent("arca-server")
    }
}
