LLAMA_DIR    := $(HOME)/llama.cpp
LLAMA_BUILD  := $(LLAMA_DIR)/build

LLAMA_LIB    := $(LLAMA_BUILD)/src/libllama.a
GGML_LIB     := $(LLAMA_BUILD)/ggml/src/libggml.a
GGML_CPU_LIB := $(LLAMA_BUILD)/ggml/src/libggml-cpu.a
GGML_BASE    := $(LLAMA_BUILD)/ggml/src/libggml-base.a
GGML_METAL   := $(LLAMA_BUILD)/ggml/src/ggml-metal/libggml-metal.a
GGML_BLAS    := $(LLAMA_BUILD)/ggml/src/ggml-blas/libggml-blas.a

LLAMA_INCLUDE := $(LLAMA_DIR)/include
GGML_INCLUDE  := $(LLAMA_DIR)/ggml/include

CGO_LDFLAGS := -L$(LLAMA_BUILD)/src \
               -L$(LLAMA_BUILD)/ggml/src \
               -L$(LLAMA_BUILD)/ggml/src/ggml-metal \
               -L$(LLAMA_BUILD)/ggml/src/ggml-blas \
               -lllama -lggml -lggml-cpu -lggml-base -lggml-metal -lggml-blas \
               -lc++ \
               -framework Metal -framework Foundation -framework Accelerate

CGO_CFLAGS := -I$(LLAMA_INCLUDE) -I$(GGML_INCLUDE)

.PHONY: llama-build backend bundle dist backend-stub clean

## Build llama.cpp static libraries (run once after cloning)
llama-build:
	cmake -B $(LLAMA_BUILD) -S $(LLAMA_DIR) \
	  -DLLAMA_METAL=ON \
	  -DBUILD_SHARED_LIBS=OFF \
	  -DGGML_NATIVE=OFF \
	  -DCMAKE_OSX_ARCHITECTURES=arm64 \
	  -DLLAMA_BUILD_TESTS=OFF \
	  -DLLAMA_BUILD_EXAMPLES=OFF \
	  -DLLAMA_BUILD_SERVER=OFF
	cmake --build $(LLAMA_BUILD) --config Release -j8 --target llama

## Build the Go backend binary (with llama.cpp, sqlite-vec, and DuckDB linked in)
backend:
	cd backend && \
	  CGO_ENABLED=1 \
	  CGO_CFLAGS="$(CGO_CFLAGS)" \
	  CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	  go build -tags llamacpp,sqlitevec,duckdb -o arca-server ./cmd/server

## Copy the backend binary into the Swift package's Resources (used by swift build)
bundle: backend
	mkdir -p app/Sources/Arca/Resources
	cp backend/arca-server app/Sources/Arca/Resources/arca-server

## Assemble a self-contained Arca.app in dist/
dist: bundle
	rm -rf dist/Arca.app
	mkdir -p dist/Arca.app/Contents/MacOS
	mkdir -p dist/Arca.app/Contents/Resources
	cd app && swift build -c release 2>&1
	cp app/.build/arm64-apple-macosx/release/Arca dist/Arca.app/Contents/MacOS/Arca
	cp backend/arca-server dist/Arca.app/Contents/Resources/arca-server
	cp app/Info.plist dist/Arca.app/Contents/Info.plist
	chmod +x dist/Arca.app/Contents/MacOS/Arca
	chmod +x dist/Arca.app/Contents/Resources/arca-server
	codesign --force --deep --sign - dist/Arca.app
	@echo "Built: dist/Arca.app"

## Build without CGo (stubs only — for testing the server/API layer)
backend-stub:
	cd backend && go build -o arca-server ./cmd/server

clean:
	rm -f backend/arca-server app/Sources/Arca/Resources/arca-server
	rm -rf dist/
