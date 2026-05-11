package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/embed/nomic"
	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/llm/llamacpp"
	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/sqlengine"
	"github.com/ameetmadan/arca/pkg/sqlengine/duckdb"
	"github.com/ameetmadan/arca/pkg/vector"
	"github.com/ameetmadan/arca/pkg/vector/sqlitevec"
)

// File is the YAML configuration schema.
type File struct {
	Server struct {
		Port int `yaml:"port"`
	} `yaml:"server"`

	LLM struct {
		Provider  string `yaml:"provider"`
		ModelPath string `yaml:"model_path"`
	} `yaml:"llm"`

	Embed struct {
		Provider  string `yaml:"provider"`
		ModelPath string `yaml:"model_path"`
	} `yaml:"embed"`

	Vector struct {
		Provider string `yaml:"provider"`
		DBPath   string `yaml:"db_path"`
	} `yaml:"vector"`

	SQL struct {
		Provider string `yaml:"provider"`
		DBPath   string `yaml:"db_path"`
	} `yaml:"sql"`

	Watcher struct {
		Folders []string `yaml:"folders"`
	} `yaml:"watcher"`

	Models struct {
		Dir string `yaml:"dir"`
	} `yaml:"models"`
}

// App holds all wired provider instances.
type App struct {
	Cfg           *File
	ConfigPath    string
	LLMProvider   llm.LLMProvider
	EmbedProvider embed.EmbeddingProvider
	VectorStore   vector.VectorStore
	SQLEngine     sqlengine.SQLEngine
	Parser        parser.DocumentParser
}

// PersistModelPaths rewrites config.yaml with updated LLM and embed model paths.
func (a *App) PersistModelPaths(llmPath, embedPath string) error {
	if a.ConfigPath == "" {
		return nil
	}
	data, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	tilde := func(p string) string {
		if filepath.HasPrefix(p, home) {
			return "~" + p[len(home):]
		}
		return p
	}
	if llmPath != "" {
		if llm, ok := raw["llm"].(map[string]interface{}); ok {
			llm["model_path"] = tilde(llmPath)
		}
	}
	if embedPath != "" {
		if emb, ok := raw["embed"].(map[string]interface{}); ok {
			emb["model_path"] = tilde(embedPath)
		}
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(a.ConfigPath, out, 0o644)
}

func Load(path string) (*App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg File
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse yaml: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 44742
	}

	expandPaths(&cfg)

	app := &App{Cfg: &cfg, ConfigPath: path}

	app.LLMProvider, err = wireLLM(&cfg)
	if err != nil {
		return nil, err
	}
	app.EmbedProvider, err = wireEmbed(&cfg)
	if err != nil {
		return nil, err
	}
	app.VectorStore, err = wireVector(&cfg)
	if err != nil {
		return nil, err
	}
	app.SQLEngine, err = wireSQL(&cfg, app.LLMProvider)
	if err != nil {
		return nil, err
	}
	app.Parser = parser.NewMultiParser()

	return app, nil
}

func expandPaths(cfg *File) {
	home, _ := os.UserHomeDir()
	exp := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	cfg.LLM.ModelPath = exp(cfg.LLM.ModelPath)
	cfg.Embed.ModelPath = exp(cfg.Embed.ModelPath)
	cfg.Vector.DBPath = exp(cfg.Vector.DBPath)
	cfg.SQL.DBPath = exp(cfg.SQL.DBPath)
	cfg.Models.Dir = exp(cfg.Models.Dir)
	for i, f := range cfg.Watcher.Folders {
		cfg.Watcher.Folders[i] = exp(f)
	}
}

func wireLLM(cfg *File) (llm.LLMProvider, error) {
	switch cfg.LLM.Provider {
	case "llamacpp", "":
		return llamacpp.New(cfg.LLM.ModelPath)
	default:
		return nil, fmt.Errorf("config: unknown llm provider %q", cfg.LLM.Provider)
	}
}

func wireEmbed(cfg *File) (embed.EmbeddingProvider, error) {
	switch cfg.Embed.Provider {
	case "nomic", "":
		return nomic.New(cfg.Embed.ModelPath)
	default:
		return nil, fmt.Errorf("config: unknown embed provider %q", cfg.Embed.Provider)
	}
}

func wireVector(cfg *File) (vector.VectorStore, error) {
	switch cfg.Vector.Provider {
	case "sqlitevec", "":
		return sqlitevec.New(cfg.Vector.DBPath)
	default:
		return nil, fmt.Errorf("config: unknown vector provider %q", cfg.Vector.Provider)
	}
}

func wireSQL(cfg *File, llmProvider llm.LLMProvider) (sqlengine.SQLEngine, error) {
	switch cfg.SQL.Provider {
	case "duckdb", "":
		return duckdb.New(cfg.SQL.DBPath, llmProvider)
	default:
		return nil, fmt.Errorf("config: unknown sql provider %q", cfg.SQL.Provider)
	}
}
