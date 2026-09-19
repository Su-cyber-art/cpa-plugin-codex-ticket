GO ?= go
.PHONY: test build clean

test:
	GOMAXPROCS=2 $(GO) test -p 2 -race ./... -count=1 -timeout=90s
	GOMAXPROCS=2 $(GO) vet ./...

build:
	mkdir -p dist
	CGO_ENABLED=1 GOMAXPROCS=2 $(GO) build -trimpath -ldflags='-s -w' -buildmode=c-shared -o dist/codex-ticket.so ./cmd/codex-ticket

clean:
	rm -f dist/codex-ticket.so dist/codex-ticket.h
