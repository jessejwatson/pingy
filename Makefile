VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
ifeq ($(VERSION),)
VERSION := dev
endif
PREFIX  ?= $(HOME)/.local
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test install uninstall dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/pingy ./cmd/pingy

test:
	go vet ./...
	go test ./...

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/pingy $(PREFIX)/bin/pingy

uninstall:
	rm -f $(PREFIX)/bin/pingy

# Source tarball in the layout a Homebrew formula expects.
dist:
	rm -rf dist && mkdir -p dist/pingy-$(VERSION)
	tar --exclude=./dist --exclude=./bin --exclude=./.git -cf - . | tar -xf - -C dist/pingy-$(VERSION)
	tar -czf dist/pingy-$(VERSION).tar.gz -C dist pingy-$(VERSION)
	rm -rf dist/pingy-$(VERSION)
	@shasum -a 256 dist/pingy-$(VERSION).tar.gz

clean:
	rm -rf bin dist
