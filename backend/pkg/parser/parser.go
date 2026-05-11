package parser

type Chunk struct {
	Content  string
	Metadata map[string]interface{}
}

type DocumentParser interface {
	Parse(path string) ([]Chunk, error)
}
