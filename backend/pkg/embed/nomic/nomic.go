//go:build !llamacpp

// Stub — compile without -tags llamacpp for API-layer testing.
package nomic

import (
	"context"
	"errors"
)

var ErrNotBuilt = errors.New("nomic: binary not built with llama.cpp CGo bindings (build tag: llamacpp)")

type NomicEmbedder struct{ ModelPath string }

func New(modelPath string) (*NomicEmbedder, error) {
	return &NomicEmbedder{ModelPath: modelPath}, nil
}

func (e *NomicEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, ErrNotBuilt
}

func (e *NomicEmbedder) UpdateModelPath(path string) { e.ModelPath = path }
