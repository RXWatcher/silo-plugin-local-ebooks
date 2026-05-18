BINARY := continuum-plugin-local-ebooks
GO ?= go
PNPM ?= pnpm

.PHONY: build web test fmt clean
web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run build
build: web
	$(GO) build -o $(BINARY) ./cmd/continuum-plugin-local-ebooks
test:
	$(GO) test ./...
fmt:
	$(GO) fmt ./...
clean:
	rm -f $(BINARY)
