# hmt

A CLI tool that analyzes Claude Code and OpenAI Codex CLI token usage and estimates costs from JSONL session logs in `~/.claude/projects/` and `~/.codex/sessions/`. Outputs table, JSON, CSV, or chart reports.

## Architecture

- `main.go` — CLI entry point, flag parsing, orchestration
- `internal/parser/` — Claude and Codex JSONL scanning, line parsing, cross-file dedup, project name extraction
- `internal/report/` — filtering, aggregation, cost computation, output formatting (table/JSON/CSV/chart)
- `internal/pricing/` — pricing table loading (fresh cache, LiteLLM refresh, stale-cache fallback), cost calculation

Data flow: scan → parse → dedup → filter → aggregate → cost → format

## Design Principles

**KISS** — Prefer the simplest solution that works. No speculative abstractions. A function, type, or package should exist only if it earns its place. Flat is better than nested; inline is better than indirected — until it isn't.

**DRY** — Extract shared logic only when duplication is real and repeated, not speculative. Three similar lines are better than a premature helper. When you do extract, the abstraction must be simpler to use than the code it replaces.

One consequence worth stating, so it is not mistaken for drift: each package carries its own `capture*Stderr` test helper, four in total. Go cannot share unexported test helpers across packages, so the alternative is an `internal/testutil` package existing solely for one function — which costs more than the duplication it removes. Keep them duplicated; keep them identical.

## Conventions

- Worktrees must be created under the project `.worktrees/` directory.
