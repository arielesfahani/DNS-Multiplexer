.PHONY: build build-linux-amd64 build-linux-arm64 build-all clean download-findns

FINDNS_VERSION = v0.2.2.1
FINDNS_REPO = SamNet-dev/findns

# Build for current platform (requires embedded/slipnet to be placed manually)
build:
	go build -o bin/dns-multiplexer

# Download findns binaries from GitHub releases
download-findns:
	@echo "Downloading findns $(FINDNS_VERSION)..."
	mkdir -p bin
	curl -fsSL "https://github.com/$(FINDNS_REPO)/releases/download/$(FINDNS_VERSION)/findns-linux-amd64" -o bin/findns-linux-amd64
	curl -fsSL "https://github.com/$(FINDNS_REPO)/releases/download/$(FINDNS_VERSION)/findns-linux-arm64" -o bin/findns-linux-arm64
	chmod +x bin/findns-linux-amd64 bin/findns-linux-arm64
	@echo "Done."

# Build for Linux amd64 with bundled slipnet + findns
build-linux-amd64:
	cp bin/slipnet-linux-amd64 embedded/slipnet
	-cp bin/findns-linux-amd64 embedded/findns 2>/dev/null || true
	GOOS=linux GOARCH=amd64 go build -o bin/dns-multiplexer-linux-amd64
	rm -f embedded/slipnet embedded/findns

# Build for Linux arm64 with bundled slipnet + findns
build-linux-arm64:
	cp bin/slipnet-linux-arm64 embedded/slipnet
	-cp bin/findns-linux-arm64 embedded/findns 2>/dev/null || true
	GOOS=linux GOARCH=arm64 go build -o bin/dns-multiplexer-linux-arm64
	rm -f embedded/slipnet embedded/findns

# Build both Linux targets
build-all: build-linux-amd64 build-linux-arm64

clean:
	rm -f embedded/slipnet embedded/findns
	rm -f bin/dns-multiplexer bin/dns-multiplexer-linux-amd64 bin/dns-multiplexer-linux-arm64
