.PHONY: build test test-v lint clean

build:
	go build -o bin/audit-service ./cmd/audit
	go build -o bin/mcp-server ./cmd/mcp

test:
	go test ./...

test-v:
	go test -v ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
