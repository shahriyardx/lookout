BINARY  := lookout
VERSION ?= 1.0.0
LDFLAGS := -s -w -X main.version=$(VERSION)
TW      := ./node_modules/.bin/tailwindcss

.PHONY: build css run linux linux-arm64 deps clean

## build: compile the binary for the host platform (uses the committed app.css)
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

## css: recompile the embedded Tailwind stylesheet (needs `make deps` once)
css:
	$(TW) -i styles/input.css -o web/app.css --minify

## run: build then launch on :7070
run: build
	./bin/$(BINARY)

## linux: static amd64 build for a Linux server (no cgo, fully self-contained)
linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .

## linux-arm64: static arm64 build (Raspberry Pi 64-bit, ARM servers)
linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

## deps: install the build-time CSS toolchain (Node)
deps:
	npm install

## clean: remove build artifacts
clean:
	rm -rf bin
