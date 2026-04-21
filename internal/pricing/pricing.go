package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ModelPricing holds per-token costs for a single model.
type ModelPricing struct {
	InputCostPerToken      float64
	OutputCostPerToken     float64
	CacheWriteCostPerToken float64
	CacheReadCostPerToken  float64
}

// Table maps model names to their pricing.
type Table struct {
	models map[string]ModelPricing
}

// Lookup returns the pricing for the given model name.
func (t *Table) Lookup(model string) (ModelPricing, bool) {
	p, ok := t.models[model]
	return p, ok
}

// jsonEntry matches the LiteLLM pricing JSON structure.
type jsonEntry struct {
	InputCostPerToken      float64 `json:"input_cost_per_token"`
	OutputCostPerToken     float64 `json:"output_cost_per_token"`
	CacheWriteCostPerToken float64 `json:"cache_creation_input_token_cost"`
	CacheReadCostPerToken  float64 `json:"cache_read_input_token_cost"`
}

// LoadFromJSON parses a pricing JSON blob (LiteLLM format, filtered to
// Anthropic models) into a Table.
func LoadFromJSON(data []byte) (*Table, error) {
	var raw map[string]jsonEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing pricing JSON: %w", err)
	}
	t := &Table{models: make(map[string]ModelPricing, len(raw))}
	for name, entry := range raw {
		t.models[name] = ModelPricing{
			InputCostPerToken:      entry.InputCostPerToken,
			OutputCostPerToken:     entry.OutputCostPerToken,
			CacheWriteCostPerToken: entry.CacheWriteCostPerToken,
			CacheReadCostPerToken:  entry.CacheReadCostPerToken,
		}
	}
	return t, nil
}

// Load tries the cached pricing file first, falls back to embedded data.
func Load(cachedPath string, fallbackData []byte) (*Table, error) {
	if data, err := os.ReadFile(cachedPath); err == nil {
		table, err := LoadFromJSON(data)
		if err == nil {
			return table, nil
		}
		fmt.Fprintf(os.Stderr, "warning: cached pricing invalid, using fallback: %v\n", err)
	}
	return LoadFromJSON(fallbackData)
}

// isFresh returns true if the file at path exists and was modified less than maxAge ago.
func isFresh(path string, maxAge time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < maxAge
}

// fetchAndFilter downloads the full LiteLLM pricing JSON from url,
// filters to Anthropic models, and writes the result to outPath.
// Creates parent directories as needed.
func fetchAndFilter(url, outPath string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetching pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching pricing: status %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading pricing response: %w", err)
	}

	type rawEntry struct {
		Provider               string  `json:"litellm_provider"`
		InputCostPerToken      float64 `json:"input_cost_per_token"`
		OutputCostPerToken     float64 `json:"output_cost_per_token"`
		CacheWriteCostPerToken float64 `json:"cache_creation_input_token_cost"`
		CacheReadCostPerToken  float64 `json:"cache_read_input_token_cost"`
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("parsing pricing JSON: %w", err)
	}

	filtered := make(map[string]jsonEntry)
	for name, rawVal := range raw {
		var e rawEntry
		if err := json.Unmarshal(rawVal, &e); err != nil {
			continue
		}
		if e.Provider != "anthropic" {
			continue
		}
		filtered[name] = jsonEntry{
			InputCostPerToken:      e.InputCostPerToken,
			OutputCostPerToken:     e.OutputCostPerToken,
			CacheWriteCostPerToken: e.CacheWriteCostPerToken,
			CacheReadCostPerToken:  e.CacheReadCostPerToken,
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	out, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pricing: %w", err)
	}

	return os.WriteFile(outPath, out, 0o644)
}

// Cost calculates the total cost for the given token counts and model pricing.
func Cost(p ModelPricing, input, output, cacheWrite, cacheRead int64) float64 {
	return float64(input)*p.InputCostPerToken +
		float64(output)*p.OutputCostPerToken +
		float64(cacheWrite)*p.CacheWriteCostPerToken +
		float64(cacheRead)*p.CacheReadCostPerToken
}
