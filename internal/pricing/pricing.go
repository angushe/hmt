package pricing

import (
	"encoding/json"
	"fmt"
	"os"
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

// Cost calculates the total cost for the given token counts and model pricing.
func Cost(p ModelPricing, input, output, cacheWrite, cacheRead int64) float64 {
	return float64(input)*p.InputCostPerToken +
		float64(output)*p.OutputCostPerToken +
		float64(cacheWrite)*p.CacheWriteCostPerToken +
		float64(cacheRead)*p.CacheReadCostPerToken
}
