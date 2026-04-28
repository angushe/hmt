# hmt

Analyze Claude Code token usage and estimate costs from JSONL session logs.

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
| `--since` | | Start date (inclusive), `YYYY-MM-DD` |
| `--until` | | End date (inclusive), `YYYY-MM-DD` |
| `--last` | `1m` | Recent period: `7d`, `30d`, `3m`. Default applies only when neither `--since` nor `--until` is set. Mutually exclusive with them otherwise |
| `--model` | | Filter by model name |
| `--project` | | Filter by project directory (substring match) |
| `--format` | `table` | Output: `table`, `json`, `csv`, `chart` |
| `--timezone` | local | IANA timezone for date grouping, e.g. `Asia/Shanghai` |
| `--height` | `16` | Chart plot height in rows (`chart` format only; min 6) |
| `--top` | `6` | Max distinct model stacks in chart (`chart` format only; min 1) |

```
hmt version
```

## Output Formats

- **table** — colored, aligned columns with totals row
- **json** — pretty-printed array of objects
- **csv** — RFC 4180, suitable for piping to other tools
- **chart** — vertical stacked bar chart, cost on y-axis, models stacked. Requires a color terminal; falls back to `table` when piped or when `NO_COLOR` is set. Use `FORCE_COLOR=1` to override.

## Pricing

Cost estimates are based on pricing data fetched from LiteLLM and cached at `~/.config/hmt/pricing.json` for 24 hours.
