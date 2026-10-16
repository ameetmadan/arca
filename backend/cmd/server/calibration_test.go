package main

import (
	"context"
	"fmt"
	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"path/filepath"
	"testing"
)

func TestCalibrationReadThenConfirm(t *testing.T) {
	e := newDecEnv(t)
	folders := semantic.NewMemoryStore()
	ctx := context.Background()
	folders.Put(ctx, semantic.FolderIndex{Root: e.root, Rel: "Notes", Centroid: []float32{0, 1}})
	folders.Put(ctx, semantic.FolderIndex{Root: e.root, Rel: "Reports", Centroid: []float32{1, 0}})
	cfg := &config.File{}
	cfg.Watcher.Folders = []string{e.root}
	e.srv.app = &config.App{Cfg: cfg, Folders: folders, Parser: sortParser{}, EmbedProvider: sortEmbed{}}
	writeFile(t, filepath.Join(e.root, "Notes", "a.txt"), "a note")
	got := e.do("GET", "/api/v1/calibration?limit=1", "")
	if got.status != 200 {
		t.Fatalf("GET: %+v", got)
	}
	page, _ := e.svc.List(ctx, decisions.ListFilter{})
	if page.Total != 0 {
		t.Fatal("opening calibration created decisions")
	}
	body := fmt.Sprintf(`{"root":%q,"file":"Notes/a.txt"}`, e.root)
	prepared := e.do("POST", "/api/v1/calibration/decisions", body)
	if prepared.status != 200 {
		t.Fatalf("prepare: %+v", prepared)
	}
	id := int64(prepared.data["id"].(float64))
	accepted := e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), `{"target":"Notes"}`)
	if accepted.status != 200 || accepted.data["journal_id"] != nil {
		t.Fatalf("confirm: %+v", accepted)
	}
	if bad := e.do("POST", "/api/v1/calibration/decisions", fmt.Sprintf(`{"root":%q,"file":"../escape.txt"}`, e.root)); bad.status != 400 {
		t.Fatalf("escape: %+v", bad)
	}
}
