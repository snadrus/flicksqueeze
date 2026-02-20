APP       := flicksqueeze
PKG       := github.com/snadrus/flicksqueeze
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE:= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
  -X 'main.version=$(VERSION)' \
  -X 'main.commit=$(COMMIT)' \
  -X 'main.buildDate=$(BUILD_DATE)'

.PHONY: build install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(APP) .

install:
	go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f $(APP)
