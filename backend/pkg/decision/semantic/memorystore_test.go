package semantic_test

import (
	"testing"

	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"github.com/ameetmadan/arca/pkg/decision/semantic/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) semantic.FolderStore { return semantic.NewMemoryStore() })
}
