//go:build !llamacpp

// Stub — compile without -tags llamacpp for API-layer testing.
package llamacpp

import (
	"context"
	"errors"
	"io"
)

var ErrNotBuilt = errors.New("llamacpp: binary not built with llama.cpp CGo bindings (build tag: llamacpp)")

type LlamaCppProvider struct{ ModelPath string }

func New(modelPath string) (*LlamaCppProvider, error) {
	return &LlamaCppProvider{ModelPath: modelPath}, nil
}

func (p *LlamaCppProvider) Generate(_ context.Context, _ string) (io.Reader, error) {
	return nil, ErrNotBuilt
}

func (p *LlamaCppProvider) GenerateSync(_ context.Context, _ string) (string, error) {
	return "", ErrNotBuilt
}

func (p *LlamaCppProvider) UpdateModelPath(path string) { p.ModelPath = path }
