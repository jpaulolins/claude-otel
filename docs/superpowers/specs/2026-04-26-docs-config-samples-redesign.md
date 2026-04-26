---
name: Docs & Config Samples Redesign
description: Restructure README, modularize docs, create config-samples/ with per-tool MCP + OTEL setup for Claude Code, OpenCode, Codex CLI, Cursor, and Gemini CLI.
type: project
---

# Spec: Docs & Config-Samples Redesign

## Goal

Modularize project documentation and provide ready-to-use configuration samples for
every supported AI client. The README becomes a capability overview and entry-point
hub; operational detail moves to focused sub-documents. Each client gets a
`config-samples/` subfolder mirroring the exact file paths it expects, so users can
`cp -r config-samples/<tool>/. .` to get started immediately.

---

## File Structure

```
config-samples/
├── claude-code/
│   ├── .mcp.json                    # MCP server connection (current .mcp.json)
│   └── claude-managed-settings.json # OTEL env vars + HTTP hooks (current file)
├── cursor/
│   └── .cursor/
│       └── mcp.json                 # MCP only; TODO telemetry via Go interceptor
├── opencode/
│   └── opencode.json                # MCP + OTEL env vars
├── codex/
│   ├── AGENTS.md                    # Context/instructions for Codex CLI
│   └── .env.example                 # OTEL env vars to export before running CLI
└── gemini/
    └── GEMINI.md                    # MCP only; TODO telemetry via Go interceptor

docs/
├── operations.md      # current README content (Docker, build, start.sh, queries)
├── client-setup.md    # per-tool install + MCP config + OTEL config guide
└── e2e-testing.md     # mcp-server-e2e-test.md moved here + per-client sections

README.md              # rewritten: what/why, architecture, capabilities, quick links
```

---

## README (rewritten)

Sections (in order):

1. **One-paragraph description** — what the project does and why it exists (Claude Code
   hook events → audit-service → OTLP → ClickHouse; MCP server surfaces the data back
   to any AI client).
2. **Architecture** — ASCII diagram: `Claude Code → audit-service → otel-collector →
   ClickHouse ← MCP server ← AI clients`.
3. **Capabilities** — bullet list: token & cost tracking, activity timeline, developer
   ROI, role-aware MCP tools (admin/viewer), 7 AI-native prompts, secret redaction.
4. **Quick Install (self-hosted)** — prerequisites (Docker, Go ≥1.22) + 3 commands
   (`./start.sh up-mcp`, health checks, seed); link → `docs/operations.md`.
5. **Connect to a running MCP server** — one-liner per client showing which
   `config-samples/` subfolder to copy; link → `docs/client-setup.md`.
6. **Test the MCP server** — single sentence + link → `docs/e2e-testing.md`.
7. **Security** — single sentence + link → `go-hook-mcp-api/SECURITY.md`.

---

## docs/operations.md

Content: current README with minimal edits (relative link fixes, section header
adjustments). Covers: Build (Go/Make), Run E2E (start.sh commands), Docker Compose
details, ClickHouse queries, environment variable reference.

---

## docs/client-setup.md

One section per client. Each section contains:

- **Prerequisites** — what to install.
- **MCP configuration** — which file to copy from `config-samples/<tool>/`, where
  it lives, and which env var / token to replace.
- **Telemetry configuration** — OTEL setup where supported; TODO note where not.

### Per-client telemetry plan

| Client | MCP | OTEL | Mechanism |
|--------|-----|------|-----------|
| Claude Code | ✓ | ✓ full | `claude-managed-settings.json`: `OTEL_*` env vars + `PreToolUse`/`PostToolUse` HTTP hooks → audit-service |
| OpenCode | ✓ | ✓ env vars | Standard `OTEL_*` env vars exported before `opencode` invocation |
| Codex CLI | ✓ | ✓ env vars | Standard `OTEL_*` env vars exported before `codex` invocation |
| Cursor | ✓ | ✗ | **TODO (future):** local interceptor binary (built from the existing Go audit-service cmd) acting as HTTP proxy between Cursor and the OpenAI API; emits OTLP events mirroring the hook payload schema |
| Gemini CLI | ✓ | ✗ | **TODO (future):** same interceptor approach as Cursor |

### Config file details

**claude-code/.mcp.json** — identical to current `.mcp.json`; points to
`http://localhost:8081/mcp` with Bearer token.

**claude-code/claude-managed-settings.json** — identical to current file; sets
`CLAUDE_CODE_ENABLE_TELEMETRY`, all `OTEL_*` vars, `CLAUDE_HOOK_TOKEN`, and the three
HTTP hook endpoints.

**cursor/.cursor/mcp.json** — Cursor's MCP config format; same URL and token. Includes
TODO comment block explaining the missing telemetry and pointing to the future
interceptor plan.

**opencode/opencode.json** — SST OpenCode JSON config; `mcp` block with `type: "remote"`
pointing to `http://localhost:8081/mcp`. Instructs user to also export standard `OTEL_*`
env vars (same values as Claude Code) before running `opencode`.

**codex/AGENTS.md** — markdown context file Codex CLI reads at startup; explains what
tools are available via MCP and how to invoke them. Includes OTEL env var table.

**codex/.env.example** — `export OTEL_*=...` lines mirroring Claude Code's vars; user
sources this file before running `codex`.

**gemini/GEMINI.md** — equivalent of AGENTS.md for Gemini CLI; MCP server instructions
only. Includes TODO comment block for future telemetry interceptor.

---

## docs/e2e-testing.md

Current `mcp-server-e2e-test.md` content preserved verbatim at the top (Claude Code
section). New sections appended:

- **OpenCode** — how to open the prompt picker, invoke tools by natural language, run
  slash-command prompts (syntax differs from Claude Code).
- **Codex CLI** — how to reference tools from AGENTS.md context, example queries.
- **Cursor** — MCP tool invocation via Cursor's composer; note that telemetry is not
  captured (link to TODO in client-setup.md).
- **Gemini CLI** — same pattern as Cursor.

File `mcp-server-e2e-test.md` at root is removed (or replaced by a redirect notice
pointing to `docs/e2e-testing.md`) to avoid duplication.

---

## Out of scope

- No changes to Go source code, Docker images, or test files.
- No Windsurf configuration.
- The proxy interceptor for Cursor/Gemini telemetry is a future task; this spec only
  documents the TODO anchors.

---

## Self-review

- No TBDs or incomplete sections.
- Architecture diagram in README matches the actual component set.
- Cursor and Gemini TODO blocks are consistent in both `client-setup.md` and their
  respective config files.
- `docs/operations.md` is the current README — no new content to invent, minimal
  link-fix risk.
- Scope is bounded to docs and config files only; no code changes.
