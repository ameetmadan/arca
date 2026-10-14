package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultConfigYAML is written on first launch. It names no models (the app
// downloads and activates one during onboarding) and watches no folders (the
// user picks them), so the server can start on a fresh Mac before either
// exists. Everything left out falls back to the defaults documented in the
// README.
const defaultConfigYAML = `# Arca configuration. Created on first launch; the app updates the model
# paths and watched folders. See the README for every option.
server:
  port: 44742

llm:
  provider: llamacpp
  model_path: ""

embed:
  provider: nomic
  model_path: ""

vector:
  provider: sqlitevec
  db_path: ~/Library/Application Support/Arca/vectors.db

sql:
  provider: duckdb
  db_path: ~/Library/Application Support/Arca/tables.duckdb

watcher:
  folders: []

models:
  dir: ~/Library/Application Support/Arca/models
`

// EnsureFile writes the default configuration to path if no file exists
// there yet, creating parent directories as needed. It reports whether it
// created the file. An existing file is never touched.
func EnsureFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("config: create %s: %w", filepath.Dir(path), err)
	}
	// O_EXCL: if another process created the file in the meantime, keep theirs.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: create %s: %w", path, err)
	}
	if _, err := f.WriteString(defaultConfigYAML); err != nil {
		f.Close()
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	return true, f.Close()
}

// PersistWatchedFolders rewrites watcher.folders in config.yaml. Like
// PersistModelPaths it round-trips the file through a generic map, so
// comments are lost. The running server keeps using the folders it started
// with: watched folders feed several components at start-up, so the app
// restarts the server after changing them.
func (a *App) PersistWatchedFolders(folders []string) error {
	if a.ConfigPath == "" {
		return nil
	}
	return a.rewrite(func(raw map[string]interface{}) {
		watcher, ok := raw["watcher"].(map[string]interface{})
		if !ok {
			watcher = map[string]interface{}{}
			raw["watcher"] = watcher
		}
		list := make([]interface{}, 0, len(folders))
		for _, f := range folders {
			list = append(list, tildePath(f))
		}
		watcher["folders"] = list
	})
}

// rewrite applies edit to config.yaml as a generic map and writes it back
// atomically, so a crash mid-write cannot leave a truncated config.
func (a *App) rewrite(edit func(raw map[string]interface{})) error {
	data, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		return err
	}
	raw := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil { // empty file
		raw = map[string]interface{}{}
	}
	edit(raw)
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.ConfigPath), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.ConfigPath)
}

// tildePath abbreviates a path under the home directory to ~/..., the form
// the default config uses.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
