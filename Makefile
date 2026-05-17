BINARY := continuum-plugin-local-ebooks
GO ?= go

.PHONY: build web test fmt clean
web:
	cd web && (command -v pnpm >/dev/null && pnpm install --frozen-lockfile && pnpm run build || npm install && npm run build)
build: web
	$(GO) build -o $(BINARY) ./cmd/continuum-plugin-local-ebooks
test:
	$(GO) test ./...
fmt:
	$(GO) fmt ./...
clean:
	rm -f $(BINARY)
