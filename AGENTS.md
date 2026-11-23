When you reply or any output ,Please translate to chinese
附注：说明性内容以中文为主，但代码标识、HTTP名称、配置项、路径、架构术语及业内通用英文会按准确性需要保留，不做生硬翻译。

在网络请求失败时，用 http://127.0.0.1:81 作为proxy尝试。


# Repository Guidelines

## Project Structure & Module Organization

The active Go module is `report-go/` (Go 1.24). Its `cmd/report-agent/` entry point serves the report workflow; `cmd/nl2sql/` serves natural-language SQL queries. Under `internal/`, `report/` contains Eino workflow nodes and prompts, `nl2sql/` implements query tools and validation, `client/` wraps upstream services, and `transport/` handles WebSockets. Shared settings and session handling live in `config/` and `querysession/`.

Tests sit alongside Go source as `*_test.go`. Report templates live in `templates/`, architecture notes in `docs/`, and the exported workflow schema in `report-go/graphs/`. This checkout has pending deletions of legacy Python sources and launch scripts; verify paths before using older README commands.

## Build, Test, and Development Commands

Run these from `report-go/`:

- `go build ./...` compiles all packages and checks both entry points.
- `go test ./...` runs the Go test suite.
- `go vet ./...` checks common Go mistakes.
- `gofmt -w <changed.go>` formats edited Go files.
- `REPORT_REPO_ROOT="$(pwd)/.." go run ./cmd/nl2sql` starts the query service.
- `REPORT_REPO_ROOT="$(pwd)/.." NL2SQL_URL=http://127.0.0.1:18081/nl2sql go run ./cmd/report-agent` starts reports against Go NL2SQL.

Set `REPORT_REPO_ROOT` explicitly: automatic discovery currently looks for the deleted `graph.py`. Startup also requires configured models and service resources; NL2SQL needs MySQL, a schema cache, and a reachable retrieval service. Check `/health` on ports `18080` and `18081`, but verify dependencies separately.

## Coding Style & Naming Conventions

Use standard Go formatting with tabs and `gofmt` import ordering. Keep package names lowercase, exported identifiers in PascalCase, and private identifiers in camelCase. Follow existing package boundaries; propagate contextual errors and pass cancellation through `context.Context`.

## Testing Guidelines

Use Go's `testing` package, `TestXxx` names, table-driven cases, and `httptest` for HTTP dependencies. Cover SQL restrictions, retrieval failures, and session isolation when changing query behavior. No coverage threshold is configured. Describe live-service checks separately from unit tests.

## Commit & Pull Request Guidelines

Recent commits use short action-oriented subjects, such as `Migrate ReportAgent workflows to Go and Eino`; follow that style. PRs should explain behavior changes, link relevant issues, and list validation results and required configuration. Include screenshots for workflow visualization changes. Preserve unrelated worktree edits.

## Security & Configuration

Keep credentials in the ignored root `.env` or environment variables. Never commit secrets, database dumps, local model caches, or compiled binaries. Document new settings and resource requirements alongside their consumers.
