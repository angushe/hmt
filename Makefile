VERSION    = $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE = $(shell date -u +%Y-%m-%d)
LDFLAGS    = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: build test clean install

build:
	go build -ldflags '$(LDFLAGS)' -o hmt .

test:
	go test ./...

install:
	go install -ldflags '$(LDFLAGS)' .

clean:
	rm -f hmt
