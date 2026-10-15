LLAMA_DIR    := $(HOME)/llama.cpp
LLAMA_BUILD  := $(LLAMA_DIR)/build

# Stamped into Info.plist by `make dist`; CI passes the tag (e.g. 0.1.0).
VERSION      ?= 0.0.0-dev
BUILD_NUMBER ?= 1

export MACOSX_DEPLOYMENT_TARGET ?= 14.0

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
	  -DCMAKE_OSX_DEPLOYMENT_TARGET=$(MACOSX_DEPLOYMENT_TARGET) \
	  -DCMAKE_BUILD_TYPE=Release \
	  -DLLAMA_BUILD_TESTS=OFF \
	  -DLLAMA_BUILD_EXAMPLES=OFF \
	  -DLLAMA_BUILD_TOOLS=OFF \
	  -DLLAMA_BUILD_SERVER=OFF \
	  -DLLAMA_BUILD_COMMON=OFF \
	  -DLLAMA_BUILD_APP=OFF \
	  -DLLAMA_OPENSSL=OFF \
	  -DLLAMA_CURL=OFF
	cmake --build $(LLAMA_BUILD) --config Release -j8 --target llama

## Build the Go backend binary (with llama.cpp, sqlite-vec, and DuckDB linked in)
backend:
	cd backend && \
	  CGO_ENABLED=1 \
	  CGO_CFLAGS="$(CGO_CFLAGS)" \
	  CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	  go build -tags llamacpp,sqlitevec,duckdb -ldflags "-X main.version=$(VERSION)" -o arca-server ./cmd/server

## Copy the backend binary next to the Swift package so `swift run` finds it
bundle: backend
	cp backend/arca-server app/arca-server

## Assemble a self-contained, ad-hoc signed Arca.app in dist/
dist: backend
	rm -rf dist/Arca.app
	mkdir -p dist/Arca.app/Contents/MacOS dist/Arca.app/Contents/Resources
	cd app && swift build -c release --arch arm64
	cp "$$(cd app && swift build -c release --arch arm64 --show-bin-path)/Arca" dist/Arca.app/Contents/MacOS/Arca
	cp backend/arca-server dist/Arca.app/Contents/Resources/arca-server
	cp app/Info.plist dist/Arca.app/Contents/Info.plist
	cp app/AppIcon.icns dist/Arca.app/Contents/Resources/AppIcon.icns
	/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $(VERSION)" dist/Arca.app/Contents/Info.plist
	/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $(BUILD_NUMBER)" dist/Arca.app/Contents/Info.plist
	chmod +x dist/Arca.app/Contents/MacOS/Arca dist/Arca.app/Contents/Resources/arca-server
	# Sign inside-out: the nested backend first, then the bundle.
	codesign --force --sign - --identifier com.ameetmadan.Arca.server dist/Arca.app/Contents/Resources/arca-server
	codesign --force --sign - dist/Arca.app
	codesign --verify --strict --verbose=2 dist/Arca.app
	@echo "Built: dist/Arca.app ($(VERSION))"

## Build without CGo (stubs only — for testing the server/API layer)
backend-stub:
	cd backend && go build -ldflags "-X main.version=$(VERSION)" -o arca-server ./cmd/server

clean:
	rm -f backend/arca-server app/arca-server
	rm -rf dist/
