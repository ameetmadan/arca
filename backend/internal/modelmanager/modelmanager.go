package modelmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Model describes a downloadable .gguf model.
type Model struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	SHA256 string  `json:"sha256"`
	SizeGB float64 `json:"size_gb"`
}

// ProgressEvent is streamed as SSE during download.
type ProgressEvent struct {
	ModelID    string  `json:"model_id"`
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Percent    float64 `json:"percent"`
	Done       bool    `json:"done"`
	Error      string  `json:"error,omitempty"`
}

var AvailableModels = []Model{
	{
		ID:     "llama32-3b",
		Name:   "Llama 3.2 3B Instruct",
		URL:    "https://huggingface.co/bartowski/Llama-3.2-3B-Instruct-GGUF/resolve/main/Llama-3.2-3B-Instruct-Q4_K_M.gguf",
		SHA256: "",
		SizeGB: 2.0,
	},
	{
		ID:     "qwen25-3b",
		Name:   "Qwen 2.5 3B Instruct",
		URL:    "https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf",
		SHA256: "",
		SizeGB: 1.9,
	},
	// Embedding model — downloaded automatically alongside any LLM choice.
	{
		ID:     "nomic-embed",
		Name:   "Nomic Embed Text v1.5 (embeddings)",
		URL:    "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/main/nomic-embed-text-v1.5.Q4_K_M.gguf",
		SHA256: "",
		SizeGB: 0.08,
	},
}

type Manager struct {
	Dir string
}

func New(dir string) *Manager {
	return &Manager{Dir: dir}
}

func (m *Manager) ModelsDir() string { return m.Dir }

func (m *Manager) IsPresent(modelID string) bool {
	_, err := os.Stat(m.modelPath(modelID))
	return err == nil
}

func (m *Manager) ModelPath(modelID string) string {
	return m.modelPath(modelID)
}

func (m *Manager) modelPath(modelID string) string {
	for _, mod := range AvailableModels {
		if mod.ID == modelID {
			return filepath.Join(m.Dir, filepath.Base(mod.URL))
		}
	}
	return ""
}

// Download streams the model file and emits ProgressEvents via the returned channel.
// The caller should drain the channel; it is closed when done or on error.
func (m *Manager) Download(ctx context.Context, modelID string) (<-chan ProgressEvent, error) {
	var target *Model
	for i := range AvailableModels {
		if AvailableModels[i].ID == modelID {
			target = &AvailableModels[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("modelmanager: unknown model %q", modelID)
	}

	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("modelmanager: create models dir: %w", err)
	}

	dest := filepath.Join(m.Dir, filepath.Base(target.URL))
	ch := make(chan ProgressEvent, 4)

	go func() {
		defer close(ch)
		if err := m.downloadFile(ctx, target, dest, ch); err != nil {
			// Never block on a consumer that has gone away (client
			// disconnect or server shutdown cancels ctx).
			select {
			case ch <- ProgressEvent{ModelID: modelID, Error: err.Error()}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

func (m *Manager) downloadFile(ctx context.Context, model *Model, dest string, ch chan<- ProgressEvent) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, model.URL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("modelmanager: HTTP %d from %s", resp.StatusCode, model.URL)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	h := sha256.New()
	pr := &progressReader{
		r:       resp.Body,
		total:   resp.ContentLength,
		modelID: model.ID,
		ch:      ch,
		hasher:  h,
	}

	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		return err
	}
	f.Close()

	if model.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != model.SHA256 {
			return fmt.Errorf("modelmanager: sha256 mismatch: got %s want %s", got, model.SHA256)
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		return err
	}

	select {
	case ch <- ProgressEvent{ModelID: model.ID, Downloaded: pr.downloaded, Total: pr.total, Percent: 100, Done: true}:
	case <-ctx.Done():
	}
	return nil
}

// SSEWrite formats a ProgressEvent as an SSE data line.
func SSEWrite(w io.Writer, ev ProgressEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

type progressReader struct {
	r          io.Reader
	total      int64
	downloaded int64
	modelID    string
	ch         chan<- ProgressEvent
	hasher     io.Writer
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.downloaded += int64(n)
		p.hasher.Write(buf[:n]) //nolint:errcheck
		var pct float64
		if p.total > 0 {
			pct = float64(p.downloaded) / float64(p.total) * 100
		}
		select {
		case p.ch <- ProgressEvent{
			ModelID:    p.modelID,
			Downloaded: p.downloaded,
			Total:      p.total,
			Percent:    pct,
		}:
		default:
		}
	}
	return n, err
}
