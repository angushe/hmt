package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/angushe/hmt/internal/parser"
	"github.com/angushe/hmt/internal/pricing"
	"github.com/angushe/hmt/internal/report"
)

//go:embed pricing_fallback.json
var fallbackPricing []byte

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

const litellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "hmt: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("hmt %s (%s, %s)\n", version, commit, buildDate)
		return nil
	}

	if len(os.Args) > 1 && os.Args[1] == "update-pricing" {
		return updatePricing()
	}

	by := flag.String("by", "day", "aggregation: day, week, month, session, project")
	since := flag.String("since", "", "start date YYYY-MM-DD")
	until := flag.String("until", "", "end date YYYY-MM-DD")
	last := flag.String("last", "", "recent period: 7d, 30d, 3m")
	model := flag.String("model", "", "filter by model name")
	project := flag.String("project", "", "filter by project (fuzzy match)")
	format := flag.String("format", "table", "output: table, json, csv")
	tz := flag.String("timezone", "", "timezone for date grouping (e.g., Asia/Shanghai, UTC)")
	flag.Parse()

	if *last != "" && (*since != "" || *until != "") {
		return fmt.Errorf("--last and --since/--until are mutually exclusive")
	}

	var sinceTime, untilTime *time.Time
	if *last != "" {
		d, err := parseDuration(*last)
		if err != nil {
			return fmt.Errorf("invalid --last value %q: %w", *last, err)
		}
		t := time.Now().Add(-d)
		sinceTime = &t
	}
	if *since != "" {
		t, err := time.Parse("2006-01-02", *since)
		if err != nil {
			return fmt.Errorf("invalid --since date %q: use YYYY-MM-DD", *since)
		}
		sinceTime = &t
	}
	if *until != "" {
		t, err := time.Parse("2006-01-02", *until)
		if err != nil {
			return fmt.Errorf("invalid --until date %q: use YYYY-MM-DD", *until)
		}
		t = t.AddDate(0, 0, 1)
		untilTime = &t
	}

	var groupBy report.GroupBy
	switch *by {
	case "day":
		groupBy = report.ByDay
	case "week":
		groupBy = report.ByWeek
	case "month":
		groupBy = report.ByMonth
	case "session":
		groupBy = report.BySession
	case "project":
		groupBy = report.ByProject
	default:
		return fmt.Errorf("invalid --by value %q: use day, week, month, session, or project", *by)
	}

	// Resolve timezone
	loc := time.Local
	if *tz != "" {
		var err error
		loc, err = time.LoadLocation(*tz)
		if err != nil {
			return fmt.Errorf("invalid --timezone %q: %w", *tz, err)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	dataDir := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return fmt.Errorf("Claude Code data directory not found: %s", dataDir)
	}

	cachedPricing := filepath.Join(home, ".config", "hmt", "pricing.json")
	table, err := pricing.Load(cachedPricing, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("loading pricing: %w", err)
	}

	records, err := parser.ScanDir(dataDir)
	if err != nil {
		return fmt.Errorf("scanning data: %w", err)
	}
	if len(records) == 0 {
		fmt.Println("no data found")
		return nil
	}

	filtered := report.Filter(records, sinceTime, untilTime, *model, *project)
	if len(filtered) == 0 {
		fmt.Println("no data found matching filters")
		return nil
	}

	rows := report.Aggregate(filtered, groupBy, loc)
	report.ComputeCosts(rows, table)

	switch *format {
	case "table":
		report.FormatTable(os.Stdout, rows, *by)
	case "json":
		report.FormatJSON(os.Stdout, rows, *by)
	case "csv":
		report.FormatCSV(os.Stdout, rows, *by)
	default:
		return fmt.Errorf("invalid --format value %q: use table, json, or csv", *format)
	}

	return nil
}

func parseDuration(s string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d+)([dm])$`)
	m := re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("expected format like 7d or 3m")
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "m":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unexpected unit %q", m[2])
}

func updatePricing() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	configDir := filepath.Join(home, ".config", "hmt")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	fmt.Fprintf(os.Stderr, "fetching pricing from LiteLLM...\n")
	resp, err := http.Get(litellmURL)
	if err != nil {
		return fmt.Errorf("fetching pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("parsing LiteLLM JSON: %w", err)
	}

	type entry struct {
		Provider               string  `json:"litellm_provider"`
		InputCostPerToken      float64 `json:"input_cost_per_token"`
		OutputCostPerToken     float64 `json:"output_cost_per_token"`
		CacheWriteCostPerToken float64 `json:"cache_creation_input_token_cost"`
		CacheReadCostPerToken  float64 `json:"cache_read_input_token_cost"`
	}

	type pricingEntry struct {
		InputCostPerToken      float64 `json:"input_cost_per_token"`
		OutputCostPerToken     float64 `json:"output_cost_per_token"`
		CacheWriteCostPerToken float64 `json:"cache_creation_input_token_cost"`
		CacheReadCostPerToken  float64 `json:"cache_read_input_token_cost"`
	}

	filtered := make(map[string]pricingEntry)
	for name, rawVal := range raw {
		var e entry
		if err := json.Unmarshal(rawVal, &e); err != nil {
			continue
		}
		if e.Provider != "anthropic" {
			continue
		}
		filtered[name] = pricingEntry{
			InputCostPerToken:      e.InputCostPerToken,
			OutputCostPerToken:     e.OutputCostPerToken,
			CacheWriteCostPerToken: e.CacheWriteCostPerToken,
			CacheReadCostPerToken:  e.CacheReadCostPerToken,
		}
	}

	out, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling filtered pricing: %w", err)
	}

	outPath := filepath.Join(configDir, "pricing.json")
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	fmt.Fprintf(os.Stderr, "saved %d model prices to %s\n", len(filtered), outPath)
	return nil
}
