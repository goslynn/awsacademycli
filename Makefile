# CGO buys nothing for a pure Go CLI, and there is no C compiler here anyway,
# so it stays off: the binary comes out static and portable.
export CGO_ENABLED = 0

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build install test lint clean capture

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o awsacademy ./cmd/awsacademy

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/awsacademy

test:
	go test ./...

lint:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

# Captures the real traffic to confirm Vocareum's endpoints.
# Needs a Chromium-based browser and one manual run through the flow.
capture:
	go run ./cmd/vockit -out capture.json

clean:
	rm -f awsacademy vockit capture.json
