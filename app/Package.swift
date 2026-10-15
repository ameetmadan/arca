// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "Arca",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Arca",
            path: "Sources/Arca"
        ),
        // Pure-logic tests (JSON decoding, request shapes). Run with `swift test`;
        // `swift build` does not build this target.
        .testTarget(
            name: "ArcaTests",
            dependencies: ["Arca"],
            path: "Tests/ArcaTests"
        )
    ]
)
