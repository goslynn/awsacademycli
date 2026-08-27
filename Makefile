# CGO no aporta nada a un CLI puro Go y aquí además no hay compilador de C,
# así que se desactiva siempre: el binario sale estático y portable.
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

# Captura el tráfico real para confirmar los endpoints de Vocareum.
# Necesita un navegador basado en Chromium y hacer el flujo a mano una vez.
capture:
	go run ./cmd/vockit -out captura.json

clean:
	rm -f awsacademy vockit captura.json
