package pricing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromJSON(t *testing.T) {
	data := `{
		"claude-opus-4-6": {
			"input_cost_per_token": 5e-06,
			"output_cost_per_token": 2.5e-05,
			"cache_creation_input_token_cost": 6.25e-06,
			"cache_read_input_token_cost": 5e-07
		}
	}`
	table, err := LoadFromJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := table.Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("expected to find claude-opus-4-6")
	}
	if p.InputCostPerToken != 5e-06 {
		t.Errorf("input cost = %v, want 5e-06", p.InputCostPerToken)
	}
	if p.OutputCostPerToken != 2.5e-05 {
		t.Errorf("output cost = %v, want 2.5e-05", p.OutputCostPerToken)
	}
	if p.CacheWriteCostPerToken != 6.25e-06 {
		t.Errorf("cache write cost = %v, want 6.25e-06", p.CacheWriteCostPerToken)
	}
	if p.CacheReadCostPerToken != 5e-07 {
		t.Errorf("cache read cost = %v, want 5e-07", p.CacheReadCostPerToken)
	}
}

func TestLookup_NotFound(t *testing.T) {
	table, _ := LoadFromJSON([]byte(`{}`))
	_, ok := table.Lookup("nonexistent")
	if ok {
		t.Fatal("expected ok=false for missing model")
	}
}

func TestLoad_CachedOverFallback(t *testing.T) {
	tmp := t.TempDir()
	cachedPath := filepath.Join(tmp, "pricing.json")
	cachedData := `{"claude-opus-4-6":{"input_cost_per_token":9.99e-06,"output_cost_per_token":1e-05,"cache_creation_input_token_cost":1e-06,"cache_read_input_token_cost":1e-07}}`
	if err := os.WriteFile(cachedPath, []byte(cachedData), 0o644); err != nil {
		t.Fatal(err)
	}
	fallbackData := []byte(`{"claude-opus-4-6":{"input_cost_per_token":5e-06,"output_cost_per_token":2.5e-05,"cache_creation_input_token_cost":6.25e-06,"cache_read_input_token_cost":5e-07}}`)

	table, err := Load(cachedPath, fallbackData)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := table.Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("expected to find claude-opus-4-6")
	}
	if p.InputCostPerToken != 9.99e-06 {
		t.Errorf("input cost = %v, want 9.99e-06 (from cache)", p.InputCostPerToken)
	}
}

func TestLoad_FallbackWhenNoCached(t *testing.T) {
	fallbackData := []byte(`{"claude-opus-4-6":{"input_cost_per_token":5e-06,"output_cost_per_token":2.5e-05,"cache_creation_input_token_cost":6.25e-06,"cache_read_input_token_cost":5e-07}}`)
	table, err := Load("/nonexistent/path/pricing.json", fallbackData)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := table.Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("expected to find claude-opus-4-6 from fallback")
	}
	if p.InputCostPerToken != 5e-06 {
		t.Errorf("input cost = %v, want 5e-06 (from fallback)", p.InputCostPerToken)
	}
}
