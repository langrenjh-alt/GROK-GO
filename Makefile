.PHONY: web test build release clean

web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-web.ps1

test:
	go test ./...
	pnpm --dir web test

build: web
	go build -trimpath -o bin/grok-go ./cmd/grok-go

release: web
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-release.ps1

clean:
	powershell -NoProfile -Command "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue bin, web/out, internal/webui/dist/*"
