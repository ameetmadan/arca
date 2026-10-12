package main

import (
	"io"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	appLogger = newJSONLogger(io.Discard) // keep test output quiet
	os.Exit(m.Run())
}
