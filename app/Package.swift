// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "Arca",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Arca",
            path: "Sources/Arca",
            resources: [.process("Resources")]
        )
    ]
)
