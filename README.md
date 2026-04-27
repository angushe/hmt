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
| `--last` | | Recent period: `7d`, `30d`, `3m`. Mutually exclusive with `--since`/`--until` |
| `--model` | | Filter by model name |
| `--project` | | Filter by project directory (substring match) |
| `--format` | `table` | Output: `table`, `json`, `csv` |
| `--timezone` | local | IANA timezone for date grouping, e.g. `Asia/Shanghai` |

```
hmt version
```

## Output Formats

- **table** — colored, aligned columns with totals row
- **json** — pretty-printed array of objects
- **csv** — RFC 4180, suitable for piping to other tools

## Pricing

Cost estimates are based on pricing data fetched from LiteLLM and cached at `~/.config/hmt/pricing.json` for 24 hours.
