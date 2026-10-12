import SwiftUI

/// Short inline help for the rule expression language. The full reference is
/// the package doc of backend/pkg/decision/rules (doc.go).
struct RulesHelpView: View {
    private struct Example: Identifiable {
        let expression: String
        let note: String
        var id: String { expression }
    }

    private let examples: [Example] = [
        Example(expression: #"Ext == ".pdf""#, note: "Every PDF."),
        Example(expression: #"Ext == ".pdf" && Snippet contains "Invoice""#, note: "A PDF whose text mentions Invoice."),
        Example(expression: #"Ext in [".png", ".jpg"] && Size > 5 * 1024 * 1024"#, note: "Images larger than 5 MB (Size is in bytes)."),
        Example(expression: #"lower(RelPath) contains "screenshot""#, note: "A file name or folder containing screenshot, in any letter case."),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("An expression must be true or false. Strings use double quotes.")

            Text("File extensions include the leading dot and are lower case: \".pdf\", not \"pdf\" or \".PDF\".")
                .fontWeight(.semibold)

            VStack(alignment: .leading, spacing: 6) {
                Text("Examples").font(.subheadline.weight(.semibold))
                ForEach(examples) { example in
                    VStack(alignment: .leading, spacing: 1) {
                        Text(verbatim: example.expression)
                            .font(.system(.callout, design: .monospaced))
                            .textSelection(.enabled)
                        Text(example.note).font(.caption).foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }
            }

            VStack(alignment: .leading, spacing: 2) {
                Text("Fields").font(.subheadline.weight(.semibold))
                Text("Path, RelPath (relative to the watched folder), Ext, MIME, Size (bytes), Modified (a time), EXIF (text map), Snippet (a text excerpt).")
                    .font(.callout)
                Text("Operators: == != < > && || ! in contains startsWith endsWith matches. Functions include lower, upper, trim, hasPrefix, split, any, all, len.")
                    .font(.callout)
            }

            Text("Rules are tried from top to bottom; the first match wins.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
        .font(.callout)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
