# Spec: cotel — Unified AI Detection & Hook Auditing Binary

**Data:** 2026-04-23  
**Status:** Aguardando revisão  
**Referência:** [detect-shadow-ai.ps1](https://github.com/shamo0/AI-Detector/blob/main/detect-shadow-ai.ps1)

---

## Context

O projeto `claude-otel` já audita hooks do Claude Code via HTTP (`audit-service` na porta 8080) e expõe dados via MCP. O objetivo é:

1. **Substituir** o `audit-service` (HTTP server persistente) por um binário CLI leve invocado diretamente pelos hooks do Claude Code via `stdin`
2. **Adicionar** detecção cross-platform de AI tools (shadow AI detection) para Linux, macOS e Windows — análogo ao `detect-shadow-ai.ps1` mas em Go
3. **Preparar** um ponto de extensão (inspector) para bloqueio futuro de comandos perigosos no `pre-tool-use`

Resultado: um único binário `cotel` que substitui `audit-service` e adiciona `cotel scan`.

---

## Arquitetura

### Binário unificado: `cotel`

```
cmd/detect/main.go  →  bin/cotel  (substitui cmd/audit/)

Subcomandos:
  cotel hook                  ← recebe hook payload via stdin, emite OTEL
  cotel scan [--force]        ← detecta AI tools instalados/rodando
  cotel version
```

### Fluxo: `cotel hook`

```
stdin (JSON payload do Claude Code)
  → parse HookPayload + redact secrets
  → CommandInspector.Inspect()          ← NoopInspector por enquanto
      → emit span "cotel.hook.inspect"  ← SEMPRE (mesmo se safe)
      → Allow==false → exit 2           ← Claude Code bloqueia tool
  → emit span "cotel.hook.ingest"
  → ForceFlush() + Shutdown()
  → exit 0
```

### Fluxo: `cotel scan`

```
Resolve settings.json: ./settings.json → ~/.cotel/settings.json
  ↓
Cache check: last_run_at < 24h + sem --force → exit 0 (imprime último resultado)
  ↓
Detectores em paralelo (errgroup, timeout: scan_timeout_seconds)
  ├── claude.go
  ├── cursor.go
  ├── codex.go       ← OTEL harvest
  ├── opencode.go    ← OTEL harvest
  ├── packages.go
  └── network.go
  ↓
Agrega findings → Report
  ├── JSON no stdout + exit code
  └── OTEL emit (se endpoint configurado)
  ↓
Persiste last_run_at + summary em settings.json
```

---

## Estrutura de diretórios

```
go-hook-mcp-api/
├── cmd/
│   ├── detect/                ← NOVO (substitui cmd/audit/)
│   │   └── main.go
│   └── mcp/                   ← inalterado
├── internal/
│   ├── hook/                  ← RENOMEADO de internal/audit/
│   │   ├── payload.go         ← HookPayload struct + normalize (reaproveitado)
│   │   └── redact.go          ← secret redaction (reaproveitado, sem alteração)
│   ├── detect/
│   │   ├── detector.go        ← interface Detector + tipos Finding, Severity, Report
│   │   ├── runner.go          ← parallel runner com errgroup
│   │   ├── cache.go           ← leitura/escrita de ~/.cotel/settings.json
│   │   ├── report.go          ← JSON output + exit code + OTEL emit
│   │   ├── inspect.go         ← CommandInspector interface + NoopInspector
│   │   └── detectors/
│   │       ├── claude.go
│   │       ├── cursor.go
│   │       ├── codex.go
│   │       ├── opencode.go
│   │       ├── packages.go
│   │       └── network.go
│   ├── otelexport/            ← ajuste: SimpleSpanProcessor para processos curtos
│   └── mcp/                   ← inalterado
└── platform/
    ├── paths_darwin.go        ← build tag: darwin
    ├── paths_linux.go         ← build tag: linux
    └── paths_windows.go       ← build tag: windows
```

---

## Modelo de dados

### Interface `Detector`

```go
type Detector interface {
    Name() string
    Detect(ctx context.Context) ([]Finding, error)
}

type Severity string
const (
    SeverityInfo   Severity = "info"    // instalado, inativo
    SeverityMedium Severity = "medium"  // processo rodando / config ativa
    SeverityHigh   Severity = "high"    // transmitindo para API externa / OTEL configurado
)

type Finding struct {
    Tool     string
    Module   string            // filesystem|process|network|packages|otel-harvest
    Signal   string
    Path     string
    Severity Severity
    Metadata map[string]string
}
```

### `settings.json` (configuração + cache)

```json
{
  "version": 1,
  "otel_endpoint": "http://otel-collector:4318",
  "otel_token": "",
  "scan_timeout_seconds": 30,
  "last_run_at": "2026-04-23T10:00:00Z",
  "last_exit_code": 1,
  "last_summary": {
    "findings_count": 3,
    "tools_detected": ["cursor", "codex"]
  }
}
```

**Resolução:** `./settings.json` → `~/.cotel/settings.json` (cria se não existir)

### Report JSON (stdout de `cotel scan`)

```json
{
  "schema_version": "1.0",
  "scanned_at": "2026-04-23T10:00:00Z",
  "hostname": "macbook-joao",
  "os": "darwin",
  "arch": "arm64",
  "exit_code": 1,
  "findings": [
    {
      "tool": "cursor",
      "module": "filesystem",
      "signal": "config directory found",
      "path": "/Users/joao/.cursor",
      "severity": "info",
      "metadata": {}
    },
    {
      "tool": "codex",
      "module": "otel-harvest",
      "signal": "otel exporter configured",
      "path": "/Users/joao/.codex/config.toml",
      "severity": "high",
      "metadata": { "exporter": "otlp-http", "endpoint": "https://collector.internal" }
    }
  ],
  "summary": {
    "findings_count": 2,
    "tools_detected": ["cursor", "codex"],
    "modules_ran": ["filesystem", "process", "network", "packages", "otel-harvest"],
    "duration_ms": 1240
  }
}
```

### Exit codes

| Código | Significado |
|--------|-------------|
| `0` | Nenhum finding medium/high (limpo) |
| `1` | Um ou mais findings medium/high detectados |
| `2` | Erro de execução ou comando bloqueado pelo inspector |

Flag `--strict`: qualquer finding (incluindo `info`) = exit `1`.

---

## Inspector (placeholder)

```go
// internal/detect/inspect.go

type CommandInspector interface {
    Inspect(ctx context.Context, cmd InspectInput) InspectResult
}

type InspectInput struct {
    ToolName  string
    Command   string
    Cwd       string
    SessionID string
}

type InspectResult struct {
    Allow    bool   // false = bloquear (exit 2)
    Severity string // "safe" | "suspicious" | "dangerous"
    Reason   string
}

// NoopInspector — sempre permite. Substituir por implementação real no futuro.
// TODO: análise de padrões/ML para detecção de comandos perigosos.
type NoopInspector struct{}

func (NoopInspector) Inspect(_ context.Context, _ InspectInput) InspectResult {
    return InspectResult{Allow: true, Severity: "safe"}
}
```

Span OTEL `cotel.hook.inspect` é emitido **sempre**, independente do resultado.  
Quando `Severity == "dangerous"` (implementação futura): emite log adicional como evento crítico.

---

## Módulos de detecção

### Claude Code
- Dirs: `~/.claude/` (Linux/macOS), `%APPDATA%\Claude\` (Windows)
- Binary: `claude` no PATH
- Managed settings: `./claude-managed-settings.json`
- Severity: `info` (instalado), `medium` (processo ativo)

### Cursor
- Dirs: `~/.cursor/`, `~/.config/Cursor/` (Linux), `~/Library/Application Support/Cursor/` (macOS), `%APPDATA%\Cursor\` (Windows)
- Files: `~/.cursor/mcp.json`, `.cursorrules` no cwd
- Processos: `cursor`, `cursor-tunnel`, `Cursor`
- Rede: conexões para `api2.cursor.sh`, `repo42.cursor.sh`
- Severity: `info` (instalado), `medium` (processo), `high` (conexão ativa)

### Codex CLI (OTEL harvest)
- Dir: `~/.codex/` (todos os OSes)
- Config: `~/.codex/config.toml`, `.codex/config.toml` (projeto)
- **OTEL harvest**: lê seção `[otel]` → se `exporter != "none"` → Finding `severity: high` com metadata `{exporter, endpoint}`
- Binary: `codex` no PATH
- Severity: `info` (instalado), `high` (OTEL configurado)

### OpenCode (OTEL harvest)
- Dirs: `~/.config/opencode/` (Linux/macOS), `%USERPROFILE%\.config\opencode\` (Windows)
- Plugin cache: `~/.cache/opencode/node_modules/`
- **OTEL harvest**: detecta `opencode-plugin-otel` no cache → Finding `severity: high`
- Processos: `opencode`
- Portas: 5003 (Manager), 3000 (Web Server)
- Severity: `info` (instalado), `medium` (processo), `high` (OTEL plugin)

### Packages
- **Python**: inspeciona `site-packages` via `python3 -c "import site; print(site.getsitepackages())"`
  - Targets: `openai`, `anthropic`, `langchain`, `litellm`, `together`, `huggingface_hub`
- **Node**: `npm root -g` ou `$(npm config get prefix)/lib/node_modules`
  - Targets: `@anthropic-ai/sdk`, `openai`, `langchain`, `@google/generative-ai`
- Severity: `info`

### Network
**Active connections** — lê tabela TCP do OS:
- Linux: `/proc/net/tcp` + `/proc/net/tcp6`
- macOS: `netstat -an`
- Windows: `netstat -ano`

Domínios AI monitorados: `api.anthropic.com`, `api.openai.com`, `api2.cursor.sh`, `generativelanguage.googleapis.com`, `huggingface.co`, `ollama.ai`

**MCP port probe** — JSON-RPC probe com timeout 2s:
- Portas: `3000, 5000, 8000, 8080, 11434`
- Payload: `{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}`
- Qualquer resposta JSON válida → Finding `mcp_server_detected` severity `medium`

---

## CLI flags

```
cotel hook
  (lê evento de stdin, sem flags adicionais)

cotel scan
  --force              Ignora cache, executa mesmo que já rodou hoje
  --output json|text   Formato de saída (padrão: json)
  --modules <list>     Módulos a executar: filesystem,process,network,packages,otel-harvest
  --otel-endpoint      Sobrescreve endpoint do settings.json
  --strict             Qualquer finding (incluindo info) = exit 1
  --verbose            Log de progresso no stderr
  --config <path>      Caminho explícito para settings.json

cotel version
```

---

## Integração com projeto existente

### O que removemos
| Artefato | Motivo |
|----------|--------|
| `cmd/audit/main.go` | Substituído por `cmd/detect/main.go` |
| `internal/audit/handler.go` | HTTP handlers removidos |
| `internal/audit/middleware.go` | Auth HTTP bearer removido |
| Serviço `audit-service` no `docker-compose.yml` | Sem servidor HTTP persistente |
| Envs `AUDIT_API_TOKEN`, `AUDIT_ALLOW_ANONYMOUS` | Sem endpoint HTTP |

### Mudança em `claude-managed-settings.json`

```json
{
  "env": {
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318",
    "OTEL_SERVICE_NAME": "claude-code"
  },
  "hooks": {
    "PreToolUse": [{
      "matcher": ".*",
      "hooks": [{ "type": "command", "command": "cotel hook", "timeout": 5 }]
    }],
    "PostToolUse": [{
      "matcher": ".*",
      "hooks": [{ "type": "command", "command": "cotel hook", "timeout": 5 }]
    }],
    "PostToolUseFailure": [{
      "matcher": ".*",
      "hooks": [{ "type": "command", "command": "cotel hook", "timeout": 5 }]
    }]
  }
}
```

> **Nota:** O formato exato de command-based hooks precisa ser verificado contra a documentação do Claude Code na implementação — o projeto atual usa apenas url-based.

O tipo de evento é inferido do campo `event_type` no payload JSON recebido via `stdin`.

### Ajuste em `internal/otelexport/exporter.go`
Trocar `BatchSpanProcessor` por `SimpleSpanProcessor` para processos de curta duração — garante flush síncrono antes do `os.Exit`. Manter `Shutdown(ctx)` com deadline de 3s.

---

## Estratégia de testes

### Unit tests por detector
```
internal/detect/detectors/*_test.go
  → mock filesystem (interface os.ReadDir / os.Stat injetável)
  → fixtures: dir existe → Finding{info}; processo rodando → Finding{medium}
  → codex: config.toml com [otel] → Finding{high, metadata:{endpoint}}
  → network: mock net.Dial → resposta JSON → mcp_server_detected
```

### Unit tests — hook flow
```
cmd/detect/hook_test.go
  → pipe sample payload JSON no stdin → verifica exit 0
  → mock inspector Allow=false → verifica exit 2
  → verifica span "cotel.hook.inspect" emitido em ambos os casos
```

### Unit tests — cache
```
internal/detect/cache_test.go
  → last_run_at = agora → ShouldSkip() == true
  → last_run_at = 25h atrás → ShouldSkip() == false
  → --force → ShouldSkip() == false sempre
```

### Integration test (CI)
```makefile
test-integration:
  go run ./cmd/detect scan --output json | jq . > /dev/null
```

### Build cross-platform
```makefile
build-all:
  GOOS=linux   GOARCH=amd64  go build -o bin/cotel-linux-amd64      ./cmd/detect
  GOOS=darwin  GOARCH=arm64  go build -o bin/cotel-darwin-arm64     ./cmd/detect
  GOOS=windows GOARCH=amd64  go build -o bin/cotel-windows-amd64.exe ./cmd/detect
```

---

## Verificação end-to-end

1. `cotel version` → imprime versão, sai 0
2. `echo '{"event_type":"post_tool_use","tool_name":"Bash"}' | cotel hook` → span emitido no coletor, sai 0
3. `cotel scan --output json` → JSON válido no stdout, exit 0 ou 1
4. `cotel scan --force --output text` → output legível, persiste `last_run_at` em settings.json
5. Verificar span `cotel.hook.inspect` no ClickHouse via MCP tool `recent_logs`
6. Build cross-platform sem erro de compilação nos 3 targets

---

## Arquivos críticos a modificar

| Arquivo | Ação |
|---------|------|
| `go-hook-mcp-api/cmd/audit/` | Remover diretório |
| `go-hook-mcp-api/cmd/detect/main.go` | Criar (novo binário) |
| `go-hook-mcp-api/internal/audit/` | Renomear para `internal/hook/`, remover `handler.go` e `middleware.go` |
| `go-hook-mcp-api/internal/detect/` | Criar (todos os arquivos listados acima) |
| `go-hook-mcp-api/internal/otelexport/exporter.go` | Ajustar para SimpleSpanProcessor |
| `go-hook-mcp-api/Makefile` | Atualizar targets de build |
| `go-hook-mcp-api/docker-compose.yml` | Remover serviço audit-service |
| `claude-managed-settings.json` | URL-based → command-based hooks |
