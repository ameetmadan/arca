//go:build llamacpp

package llamacpp

/*
#cgo LDFLAGS: -framework Metal -framework Foundation -framework Accelerate

#include "llama.h"
#include <stdlib.h>
#include <string.h>

// One-time backend initialisation (safe to call multiple times).
static void arca_init() { llama_backend_init(); }

static struct llama_model* arca_load_model(const char* path, int n_gpu_layers) {
    struct llama_model_params p = llama_model_default_params();
    p.n_gpu_layers = n_gpu_layers;
    return llama_model_load_from_file(path, p);
}

static struct llama_context* arca_new_ctx(struct llama_model* m, int n_ctx, int n_batch) {
    struct llama_context_params p = llama_context_default_params();
    p.n_ctx   = (uint32_t)n_ctx;
    p.n_batch = (uint32_t)n_batch;
    return llama_init_from_model(m, p);
}

// Tokenise text; returns token count (negative on error).
static int32_t arca_tokenize(const struct llama_vocab* vocab,
                              const char* text, int32_t text_len,
                              llama_token* out, int32_t max_tokens) {
    return llama_tokenize(vocab, text, text_len, out, max_tokens, true, false);
}

// Convert a token id to its UTF-8 piece.  buf must be >= 16 bytes.
static int32_t arca_token_to_piece(const struct llama_vocab* vocab,
                                    llama_token tok, char* buf, int32_t buflen) {
    return llama_token_to_piece(vocab, tok, buf, buflen, 0, false);
}
*/
import "C"

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unsafe"
)

func init() { C.arca_init() }

// LlamaCppProvider holds a loaded model. Create one per model file.
// Contexts are created per-call so concurrent Generate calls are safe.
type LlamaCppProvider struct {
	ModelPath string
	model     *C.struct_llama_model
	vocab     *C.struct_llama_vocab
	mu        sync.Mutex // protects model loading
}

func New(modelPath string) (*LlamaCppProvider, error) {
	return &LlamaCppProvider{ModelPath: modelPath}, nil
}

// ensureModel loads the model on first call. mu must be held by caller.
func (p *LlamaCppProvider) ensureModel() error {
	if p.model != nil {
		return nil
	}
	cs := C.CString(p.ModelPath)
	defer C.free(unsafe.Pointer(cs))

	model := C.arca_load_model(cs, 99) // offload all layers to Metal
	if model == nil {
		return fmt.Errorf("llamacpp: failed to load model from %s", p.ModelPath)
	}
	p.model = model
	p.vocab = C.llama_model_get_vocab(model)
	return nil
}

func (p *LlamaCppProvider) Close() {
	if p.model != nil {
		C.llama_model_free(p.model)
		p.model = nil
	}
}

// UpdateModelPath swaps to a new model file; the next Generate call will load it.
func (p *LlamaCppProvider) UpdateModelPath(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.model != nil {
		C.llama_model_free(p.model)
		p.model = nil
		p.vocab = nil
	}
	p.ModelPath = path
}

// GenerateSync runs inference and returns the complete response.
func (p *LlamaCppProvider) GenerateSync(ctx context.Context, prompt string) (string, error) {
	var sb strings.Builder
	reader, err := p.Generate(ctx, prompt)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 512)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return sb.String(), err
		}
		if ctx.Err() != nil {
			return sb.String(), ctx.Err()
		}
	}
	return sb.String(), nil
}

// Generate runs inference and streams tokens to the returned reader.
func (p *LlamaCppProvider) Generate(ctx context.Context, prompt string) (io.Reader, error) {
	p.mu.Lock()
	err := p.ensureModel()
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}

	const nCtx = 2048
	const nBatch = 512
	const maxNewTokens = 1024

	llctx := C.arca_new_ctx(p.model, C.int(nCtx), C.int(nBatch))
	if llctx == nil {
		return nil, fmt.Errorf("llamacpp: failed to create context")
	}

	// Tokenise the prompt.
	cPrompt := C.CString(prompt)
	defer C.free(unsafe.Pointer(cPrompt))

	tokensBuf := make([]C.llama_token, nCtx)
	nTokens := C.arca_tokenize(p.vocab, cPrompt, C.int32_t(len(prompt)),
		(*C.llama_token)(unsafe.Pointer(&tokensBuf[0])), C.int32_t(nCtx))
	if nTokens < 0 {
		C.llama_free(llctx)
		return nil, fmt.Errorf("llamacpp: tokenize failed (need %d token slots)", -nTokens)
	}

	// Build sampler chain: top-k=40, top-p=0.95, temp=0.8, dist sampler.
	sparams := C.llama_sampler_chain_default_params()
	smpl := C.llama_sampler_chain_init(sparams)
	C.llama_sampler_chain_add(smpl, C.llama_sampler_init_top_k(40))
	C.llama_sampler_chain_add(smpl, C.llama_sampler_init_top_p(0.95, 1))
	C.llama_sampler_chain_add(smpl, C.llama_sampler_init_temp(0.8))
	C.llama_sampler_chain_add(smpl, C.llama_sampler_init_dist(0xDEADBEEF))

	pr, pw := io.Pipe()

	go func() {
		defer C.llama_free(llctx)
		defer C.llama_sampler_free(smpl)
		defer pw.Close()

		// Decode the prompt batch.
		batch := C.llama_batch_get_one((*C.llama_token)(unsafe.Pointer(&tokensBuf[0])), nTokens)
		if rc := C.llama_decode(llctx, batch); rc != 0 {
			pw.CloseWithError(fmt.Errorf("llamacpp: prompt decode failed (rc=%d)", rc))
			return
		}

		pieceBuf := make([]byte, 64)
		for i := 0; i < maxNewTokens; i++ {
			if ctx.Err() != nil {
				pw.CloseWithError(ctx.Err())
				return
			}

			tok := C.llama_sampler_sample(smpl, llctx, -1)
			if C.llama_vocab_is_eog(p.vocab, tok) {
				return
			}

			n := C.arca_token_to_piece(p.vocab, tok,
				(*C.char)(unsafe.Pointer(&pieceBuf[0])), C.int32_t(len(pieceBuf)))
			if n < 0 {
				// Buffer too small — resize and retry.
				pieceBuf = make([]byte, -n+1)
				n = C.arca_token_to_piece(p.vocab, tok,
					(*C.char)(unsafe.Pointer(&pieceBuf[0])), C.int32_t(len(pieceBuf)))
			}
			if n > 0 {
				if _, err := pw.Write(pieceBuf[:n]); err != nil {
					return
				}
			}

			// Feed the new token back.
			next := tok
			b := C.llama_batch_get_one((*C.llama_token)(unsafe.Pointer(&next)), 1)
			if rc := C.llama_decode(llctx, b); rc != 0 {
				pw.CloseWithError(fmt.Errorf("llamacpp: decode failed at token %d (rc=%d)", i, rc))
				return
			}
		}
	}()

	return pr, nil
}
