.DEFAULT_GOAL := help

BINARY := autodns
PKG := ./cmd/autodns
DIST := dist
LDFLAGS := -s -w

.PHONY: help test build release clean

help:
	@echo "Build:"
	@echo "  build    build local autodns binary"
	@echo "  release  build release binaries into dist/"
	@echo ""
	@echo "Test:"
	@echo "  test     run unit tests"
	@echo ""
	@echo "  clean    remove built binaries"

test:
	go test ./... -v -count=1

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(PKG)

release:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe $(PKG)
	cd $(DIST) && sha256sum * > SHA256SUMS

clean:
	rm -rf $(BINARY) $(DIST)
