.PHONY: build build-all test test-v lint clean

build:
	go build -o bin/cotel      ./cmd/detect
	go build -o bin/mcp-server ./cmd/mcp

build-all:
	GOOS=linux   GOARCH=amd64  go build -o bin/cotel-linux-amd64       ./cmd/detect
	GOOS=darwin  GOARCH=arm64  go build -o bin/cotel-darwin-arm64      ./cmd/detect
	GOOS=windows GOARCH=amd64  go build -o bin/cotel-windows-amd64.exe ./cmd/detect

test:
	go test ./...

test-v:
	go test -v ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
