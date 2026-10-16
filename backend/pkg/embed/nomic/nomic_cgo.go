//go:build llamacpp

package nomic

/*
#cgo darwin LDFLAGS: -framework Metal -framework Foundation -framework Accelerate

#include "llama.h"
#include <stdlib.h>

static void arca_embed_init() { llama_backend_init(); }

static struct llama_model* arca_embed_load(const char* path) {
    struct llama_model_params p = llama_model_default_params();
    p.n_gpu_layers = 99; // offload all layers to Metal
    return llama_model_load_from_file(path, p);
}

// Create a context configured for mean-pooled embeddings.
static struct llama_context* arca_embed_ctx(struct llama_model* m, int n_ctx) {
    struct llama_context_params p = llama_context_default_params();
    p.n_ctx          = (uint32_t)n_ctx;
    p.n_batch        = (uint32_t)n_ctx;
    p.pooling_type   = LLAMA_POOLING_TYPE_MEAN;
    p.embeddings     = true;
    return llama_init_from_model(m, p);
}

static int32_t arca_embed_tokenize(const struct llama_vocab* vocab,
                                    const char* text, int32_t text_len,
                                    llama_token* out, int32_t max_tokens) {
    return llama_tokenize(vocab, text, text_len, out, max_tokens, true, false);
}
*/
import "C"

import (
	"context"
	"fmt"
	"sync"
	"unsafe"
)

func init() { C.arca_embed_init() }

// NomicEmbedder loads nomic-embed-text-v1.5 and produces mean-pooled embeddings.
type NomicEmbedder struct {
	ModelPath string
	model     *C.struct_llama_model
	vocab     *C.struct_llama_vocab
	nEmbd     int
	mu        sync.Mutex
}

func New(modelPath string) (*NomicEmbedder, error) {
	return &NomicEmbedder{ModelPath: modelPath}, nil
}

func (e *NomicEmbedder) ensureModel() error {
	if e.model != nil {
		return nil
	}
	cs := C.CString(e.ModelPath)
	defer C.free(unsafe.Pointer(cs))

	model := C.arca_embed_load(cs)
	if model == nil {
		return fmt.Errorf("nomic: failed to load embedding model from %s", e.ModelPath)
	}
	e.model = model
	e.vocab = C.llama_model_get_vocab(model)
	e.nEmbd = int(C.llama_model_n_embd(model))
	return nil
}

func (e *NomicEmbedder) Close() {
	if e.model != nil {
		C.llama_model_free(e.model)
		e.model = nil
	}
}

// UpdateModelPath swaps to a new model file; the next Embed call will load it.
func (e *NomicEmbedder) UpdateModelPath(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model != nil {
		C.llama_model_free(e.model)
		e.model = nil
		e.vocab = nil
		e.nEmbd = 0
	}
	e.ModelPath = path
}

// Embed returns a mean-pooled embedding vector for the given text.
func (e *NomicEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	err := e.ensureModel()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}

	const maxTokens = 512
	llctx := C.arca_embed_ctx(e.model, C.int(maxTokens))
	if llctx == nil {
		return nil, fmt.Errorf("nomic: failed to create embedding context")
	}
	defer C.llama_free(llctx)

	// Prepend the nomic task prefix expected by the model.
	prefixed := "search_document: " + text

	cText := C.CString(prefixed)
	defer C.free(unsafe.Pointer(cText))

	// Tokenise everything, then keep the first maxTokens: text beyond the
	// model's window is dropped rather than failing the whole file. A token
	// never spans less than one byte, so len+16 slots are always enough.
	tokensBuf := make([]C.llama_token, len(prefixed)+16)
	nTokens := C.arca_embed_tokenize(e.vocab, cText, C.int32_t(len(prefixed)),
		(*C.llama_token)(unsafe.Pointer(&tokensBuf[0])), C.int32_t(len(tokensBuf)))
	if nTokens < 0 {
		return nil, fmt.Errorf("nomic: tokenize failed (need %d slots)", -nTokens)
	}
	if nTokens > maxTokens {
		// Keep the closing special token ([SEP]) the tokenizer appended.
		tokensBuf[maxTokens-1] = tokensBuf[nTokens-1]
		nTokens = maxTokens
	}

	// Mark the last token so the context outputs embeddings for it.
	batch := C.llama_batch_get_one((*C.llama_token)(unsafe.Pointer(&tokensBuf[0])), nTokens)

	C.llama_set_embeddings(llctx, true)
	C.llama_set_causal_attn(llctx, false)

	if rc := C.llama_encode(llctx, batch); rc != 0 {
		// Fall back to decode if encode is unsupported (decoder-only models).
		if rc2 := C.llama_decode(llctx, batch); rc2 != 0 {
			return nil, fmt.Errorf("nomic: encode/decode failed (rc=%d)", rc2)
		}
	}

	// Retrieve the mean-pooled sequence embedding.
	ptr := C.llama_get_embeddings_seq(llctx, 0)
	if ptr == nil {
		// Fall back to last-token embedding.
		ptr = C.llama_get_embeddings_ith(llctx, nTokens-1)
	}
	if ptr == nil {
		return nil, fmt.Errorf("nomic: no embeddings returned")
	}

	// Copy from C memory into a Go slice.
	out := make([]float32, e.nEmbd)
	cSlice := (*[1 << 28]C.float)(unsafe.Pointer(ptr))[:e.nEmbd:e.nEmbd]
	for i, v := range cSlice {
		out[i] = float32(v)
	}
	return out, nil
}
