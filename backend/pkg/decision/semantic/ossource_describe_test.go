package semantic

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOSSourceDescribeReplacesTextDerivation(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Taxes", "a.pdf"), "pdf bytes")
	write(t, filepath.Join(root, "Taxes", "empty.txt"), "")

	var gotPath string
	var gotMax int
	src := &OSSource{
		MaxTextBytes: 123,
		Describe: func(path string, maxBytes int) string {
			gotPath, gotMax = path, maxBytes
			return "described"
		},
	}

	got, err := src.Text(context.Background(), root, "Taxes", "a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != "described" || gotPath != filepath.Join(root, "Taxes", "a.pdf") || gotMax != 123 {
		t.Errorf("Text = %q (path %q, max %d)", got, gotPath, gotMax)
	}

	// The checks that keep Text inside the folder run before Describe does.
	gotPath = ""
	for _, name := range []string{"../a.pdf", ".hidden", "empty.txt"} {
		got, _ := src.Text(context.Background(), root, "Taxes", name)
		if got != "" || gotPath != "" {
			t.Errorf("Text(%q) = %q, Describe called with %q; want neither", name, got, gotPath)
		}
	}
}

func TestOSSourceDescribeDefaultLimit(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "A", "f.txt"), "x")
	var gotMax int
	src := &OSSource{Describe: func(_ string, maxBytes int) string { gotMax = maxBytes; return "d" }}
	if _, err := src.Text(context.Background(), root, "A", "f.txt"); err != nil {
		t.Fatal(err)
	}
	if gotMax != DefaultMaxTextBytes {
		t.Errorf("max = %d, want %d", gotMax, DefaultMaxTextBytes)
	}
}
