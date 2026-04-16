# Contributing

Thanks for your interest in contributing to this project!

## Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [Docker](https://docs.docker.com/get-docker/) and Docker Compose
- [Make](https://www.gnu.org/software/make/) (optional, for convenience targets)

## Getting started

```bash
# Clone the repository
git clone https://github.com/<owner>/claude-otel.git
cd claude-otel

# Start the infrastructure stack
./start.sh up

# Build and test the Go project
cd go-hook-mcp-api
make build
make test
```

## Project structure

```
claude-otel/
├── go-hook-mcp-api/          # Go project (self-contained)
│   ├── cmd/audit/            # Audit service entrypoint
│   ├── cmd/mcp/              # MCP server entrypoint
│   └── internal/             # audit, mcp, otelexport packages
├── clickhouse/init/          # ClickHouse schema
├── docker-compose.yml        # Full stack
├── otel-collector-config.yaml
└── start.sh                  # Stack management script
```

## Running tests

```bash
cd go-hook-mcp-api
make test       # all tests
make test-v     # verbose output
make lint       # go vet
```

## Making changes

1. Create a feature branch from `main`.
2. Write tests for new functionality.
3. Run `make test` and `make lint` before pushing.
4. Open a pull request with a clear description of the change.

## Code style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Keep functions short and focused.
- Use table-driven tests.
- No hardcoded secrets or credentials in source code.

## Reporting issues

Open an issue on GitHub with:
- Steps to reproduce
- Expected vs. actual behavior
- Go version, OS, Docker version

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
