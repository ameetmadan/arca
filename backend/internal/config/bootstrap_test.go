package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureFileCreatesLoadableDefaultOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the default config points into ~/Library
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	created, err := EnsureFile(path)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	app, err := Load(path)
	if err != nil {
		t.Fatalf("default config does not load: %v", err)
	}
	defer app.Close()
	if app.Cfg.Server.Port != 44742 || len(app.Cfg.Watcher.Folders) != 0 || app.Cfg.LLM.ModelPath != "" {
		t.Fatalf("unexpected defaults: %+v", app.Cfg)
	}

	if err := os.WriteFile(path, []byte("server:\n  port: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if created, err := EnsureFile(path); err != nil || created {
		t.Fatalf("second call: created=%v err=%v", created, err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "port: 1") {
		t.Fatal("existing config was overwritten")
	}
}

func TestPersistWatchedFoldersAndModelPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, _ := os.UserHomeDir()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	app := &App{ConfigPath: path}
	under := filepath.Join(home, "Documents")
	if err := app.PersistWatchedFolders([]string{under, "/srv/data"}); err != nil {
		t.Fatal(err)
	}
	if err := app.PersistModelPaths("/m/llm.gguf", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"~/Documents", "/srv/data", "model_path: /m/llm.gguf", "provider: nomic"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config lacks %q:\n%s", want, b)
		}
	}

	// A config without an llm section gets one rather than silently
	// dropping the path.
	os.WriteFile(path, []byte("server:\n  port: 44742\n"), 0o644)
	if err := app.PersistModelPaths("/m/llm.gguf", "/m/emb.gguf"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Cfg.LLM.ModelPath != "/m/llm.gguf" || reloaded.Cfg.Embed.ModelPath != "/m/emb.gguf" {
		t.Fatalf("paths = %q, %q", reloaded.Cfg.LLM.ModelPath, reloaded.Cfg.Embed.ModelPath)
	}
}

func TestTildePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		home:                     "~",
		filepath.Join(home, "a"): "~/a",
		home + "other/x":         home + "other/x", // a sibling dir sharing the prefix
		"/elsewhere":             "/elsewhere",
	} {
		if got := tildePath(in); got != want {
			t.Errorf("tildePath(%q) = %q, want %q", in, got, want)
		}
	}
}
