import Foundation

/// Pages on the project's GitHub site. Arca opens them in the browser and
/// never fetches anything itself, so the model download stays its only
/// network access.
enum ProjectLinks {
    static let releases = URL(string: "https://github.com/ameetmadan/arca/releases")!

    /// A new issue from `.github/ISSUE_TEMPLATE/beta-feedback.yml`. The query
    /// names are that form's field ids, which GitHub fills in from the URL.
    static func feedbackIssue(arcaVersion: String, mac: String) -> URL {
        URL(string: "https://github.com/ameetmadan/arca/issues/new")!.appending(queryItems: [
            URLQueryItem(name: "template", value: "beta-feedback.yml"),
            URLQueryItem(name: "version", value: arcaVersion),
            URLQueryItem(name: "mac", value: mac),
        ])
    }
}

extension Bundle {
    /// The release tag this build came from, e.g. "0.1.0-beta.3", as named on
    /// the Releases page. CFBundleShortVersionString only takes numbers, so
    /// `make dist` stamps the full tag into ArcaReleaseLabel.
    var releaseLabel: String { infoDictionary?["ArcaReleaseLabel"] as? String ?? "development build" }

    var buildNumber: String { infoDictionary?["CFBundleVersion"] as? String ?? "0" }
}

enum ThisMac {
    /// For bug reports, e.g. "macOS 26.1.0, Apple M2, 16 GB".
    static var summary: String {
        let os = ProcessInfo.processInfo.operatingSystemVersion
        let memoryGB = ProcessInfo.processInfo.physicalMemory / 1_073_741_824
        return "macOS \(os.majorVersion).\(os.minorVersion).\(os.patchVersion), \(chip), \(memoryGB) GB"
    }

    private static var chip: String {
        var size = 0
        guard sysctlbyname("machdep.cpu.brand_string", nil, &size, nil, 0) == 0, size > 0 else { return "unknown chip" }
        var name = [UInt8](repeating: 0, count: size)
        guard sysctlbyname("machdep.cpu.brand_string", &name, &size, nil, 0) == 0 else { return "unknown chip" }
        return String(decoding: name.prefix { $0 != 0 }, as: UTF8.self)
    }
}
