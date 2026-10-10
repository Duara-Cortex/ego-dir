BINARY    := ego-dir
PKG       := ./cmd/ego-dir
VERSION   ?= dev
DIST      := dist
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
LDFLAGS   := -s -w -X main.version=$(VERSION)

.PHONY: build test package clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) $(PKG)

test:
	go vet ./...
	go test -race ./...

# Cross-compile every platform into dist/<binary>_<version>_<os>_<arch>.tar.gz
# and write dist/checksums.txt (sha256).
package: clean
	@mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		echo "building $$name"; \
		mkdir -p $(DIST)/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/$$name/$(BINARY)$$ext $(PKG); \
		cp README.md LICENSE $(DIST)/$$name/; \
		tar -C $(DIST) -czf $(DIST)/$$name.tar.gz $$name; \
		rm -rf $(DIST)/$$name; \
	done
	@cd $(DIST) && shasum -a 256 *.tar.gz > checksums.txt
	@cat $(DIST)/checksums.txt

clean:
	rm -rf $(DIST)
