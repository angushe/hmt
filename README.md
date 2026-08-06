# hmt

Analyze Claude Code and OpenAI Codex CLI token usage from local JSONL logs and estimate equivalent API costs.

## Install

Requires Go 1.25+.

```
git clone https://github.com/angushe/hmt.git
cd hmt
make install
```

## Usage

```
hmt [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--by` | `day` | Aggregation: `day`, `week`, `month`, `session`, `project` |
| `--since` | | Start date at 00:00 UTC (inclusive), `YYYY-MM-DD`. Must not be after `--until` |
| `--until` | | End date at 24:00 UTC (inclusive through that date), `YYYY-MM-DD`. Must not be before `--since` |
| `--last` | `1m` | Recent period: `7d`, `30d`, `3m`. Default applies only when neither `--since` nor `--until` is set. Mutually exclusive with them otherwise |
| `--model` | | Filter by model name |
| `--project` | | Filter by substring of the recorded directory, rendered project name, or legacy dash-encoded identifier |
| `--format` | `table` | Output: `table`, `json`, `csv`, `chart` |
| `--timezone` | local | IANA timezone for date grouping, e.g. `Asia/Shanghai` |
| `--height` | `16` | Chart plot height in rows, 6–1000. Used by `chart` only; the range is enforced for every format. Raising it lets smaller models draw their own bar instead of merging into `other` |
| `--top` | `6` | Max distinct model stacks in chart, 1–6 (bounded by the color palette). Used by `chart` only; the range is enforced for every format. Models beyond it are merged into `other` |
| `--source` | `all` | Data source: `claude`, `codex`, or `all` |

```
hmt version
```

## Data Sources

By default, `hmt` scans both supported sources when their directories exist:

- Claude Code session logs in `~/.claude/projects/`
- Codex CLI rollout logs in `~/.codex/sessions/`

Use `--source claude` to restore Claude-only behavior, or `--source codex` to scan only Codex. Compressed Codex rollouts (`.jsonl.zst`) are reported with a warning and are not counted.

Both sources skip symlinked directories rather than following them, to avoid cycles and duplicate scans, but they differ in reach. Codex resolves its root and then warns about a symlinked directory at any depth. Claude checks only the direct children of `~/.claude/projects`, so a symlinked directory nested deeper inside a project — where subagent logs live — is skipped without a warning; a dangling nested link is silent there as well.

Under the default `--source all`, a source whose directory is missing is skipped silently, and one that exists but cannot be read is reported on stderr — but the run still exits 0 with a report covering whatever survived. Only a run where *every* selected source fails exits non-zero. A scheduled job that discards stderr therefore cannot tell a partial report from a complete one by exit code; check stderr, or pass an explicit `--source` so a failure is fatal.

Codex rows always show `Cache Write 0`: the Codex logs record no cache-write figure. In a merged table beside Claude rows carrying large cache-write numbers, that column being empty for one source is expected rather than a parsing fault.

Date filters use UTC boundaries; `--timezone` affects only day, week, and month grouping. Worktree project names use the repository plus the first path segment after `.worktrees` (or `.claude/worktrees`, which Claude Code uses for its own). Consequently, slash-named branches such as `feat/license-system` are grouped under `<repository>/feat`; this is the trade-off that keeps worktree subdirectories in one project row.

A worktree is also grouped separately from the main checkout it belongs to: `/Users/alice/work/proj` becomes `work/proj`, while `/Users/alice/work/proj/.worktrees/feat/x` becomes `proj/feat`. Under `--by project` they are two rows. `--project proj` still matches both, so only grouping is affected.

## Output Formats

- **table** — colored, aligned columns with totals row
- **json** — pretty-printed array of objects; `has_cost` identifies rows with known pricing
- **csv** — RFC 4180, suitable for piping to other tools; `has_cost` identifies rows with known pricing
- **chart** — vertical stacked bar chart, cost on y-axis, models stacked. Requires a color terminal; falls back to `table` when piped or when `NO_COLOR` is set. Use `FORCE_COLOR=1` to override. An incomplete total is marked with `*` on the total, and noted on stderr in addition. Models too small to draw a bar are folded into the gray `other` stack rather than being dropped; the legend names only what is drawn.

## Upgrading

Adding Codex as a source changed six things that existing scripts may depend on.

- **CSV gained an eighth column, `has_cost`.** Parsers that index columns positionally, or that assert exactly seven fields, need updating.
- **JSON gained a `has_cost` field.** It was previously `json:"-"` and so absent from output.
- **`--by project` keys changed for Claude users.** Project names now come from the recorded `cwd` rather than the dash-encoded directory name, which renames many rows. Saved `--project` filters and dashboards keyed on the old names will no longer match. Real names are also longer, so `--by project` output is wider and may wrap on a narrow terminal where it previously fit.
- **The all-sources-missing error text changed.** It was `Claude Code data directory not found: <path>` and is now `no data directories found: <paths>`, listing every location searched. A script matching the old string will not match the new one.
- **`--height` and `--top` are now range-checked for every format.** `hmt --top 10 --format table` previously exited 0 and printed a table; it now exits 1 with `--top must be at most 6`. The bounds exist because the chart indexes a six-colour palette directly and allocates one row per unit of height, so out-of-range values crashed or hung. Values outside `chart` are still ignored — but they are validated, so a wrapper passing an out-of-range value will now fail.
- **`--since` must now be before `--until`.** `hmt --since 2027-01-01 --until 2020-01-01` previously exited 0 with `no data found matching filters`; it now exits 1 with `--since 2027-01-01 is not before --until 2020-01-01: the range is empty`. Same-day (`--since X --until X`) is still valid and returns that day.

The first two follow from unpriced models now being possible: `codex-auto-review` has no published API price, so cost completeness has to be expressed per row. The third is a correction — the old dash-encoded names were not real paths — but it is still a rename. The fourth reflects there now being more than one directory to look for. The fifth trades a silent no-op for an error, which is the safer direction given the alternative was a panic. The sixth does the same for a range that can never match: reporting it as "no data found matching filters" reads as an answer about your data rather than a mistake in the query.

## Pricing

Cost estimates are equivalent API costs calculated from recorded tokens; they do not necessarily represent subscription-plan spend. Pricing data is fetched from LiteLLM and cached at `~/.config/hmt/pricing.json` for 24 hours; if a refresh fails, an existing stale cache is used with a warning. A cache written before OpenAI models were priced is refreshed regardless of age, so an offline run with such a cache waits for the fetch to time out on every invocation until one succeeds — the alternative would be reporting Codex usage as unpriced. In table output, models without known pricing remain `N/A`, and totals that omit such models are marked with `*`. JSON and CSV expose the same distinction through `has_cost`. Codex's internal `codex-auto-review` alias has no independently published API price, so its rows intentionally remain `N/A` rather than being assigned a guessed model rate.
