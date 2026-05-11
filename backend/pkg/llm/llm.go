package llm

import (
	"context"
	"io"
)

type LLMProvider interface {
	Generate(ctx context.Context, prompt string) (io.Reader, error)
	GenerateSync(ctx context.Context, prompt string) (string, error)
}
