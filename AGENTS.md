# hmt

A CLI tool that analyzes Claude Code token usage and estimates costs from JSONL session logs in `~/.claude/projects/`. Outputs table, JSON, or CSV.

## Architecture

- `main.go` — CLI entry point, flag parsing, orchestration
- `internal/parser/` — JSONL scanning, line parsing, dedup, project name extraction
- `internal/report/` — filtering, aggregation, cost computation, output formatting (table/JSON/CSV)
- `internal/pricing/` — pricing table loading (cached file + embedded fallback), cost calculation

Data flow: scan → parse → dedup → filter → aggregate → cost → format

## Design Principles

**KISS** — Prefer the simplest solution that works. No speculative abstractions. A function, type, or package should exist only if it earns its place. Flat is better than nested; inline is better than indirected — until it isn't.

**DRY** — Extract shared logic only when duplication is real and repeated, not speculative. Three similar lines are better than a premature helper. When you do extract, the abstraction must be simpler to use than the code it replaces.

## Conventions

- Worktrees must be created under the project `.worktrees/` directory.
