package pricing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestIsFresh_FreshFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "pricing.json")
	os.WriteFile(path, []byte(`{}`), 0o644)

	if !isFresh(path, 1*time.Hour) {
		t.Fatal("expected fresh file to be fresh")
	}
}

func TestIsFresh_ExpiredFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "pricing.json")
	os.WriteFile(path, []byte(`{}`), 0o644)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(path, old, old)

	if isFresh(path, 1*time.Hour) {
		t.Fatal("expected expired file to not be fresh")
	}
}

func TestIsFresh_MissingFile(t *testing.T) {
	if isFresh("/nonexistent/pricing.json", 1*time.Hour) {
		t.Fatal("expected missing file to not be fresh")
	}
}

func TestFetchAndFilter_Success(t *testing.T) {
	payload := map[string]any{
		"claude-opus-4-6": map[string]any{
			"litellm_provider":                "anthropic",
			"input_cost_per_token":            5e-06,
			"output_cost_per_token":           2.5e-05,
			"cache_creation_input_token_cost": 6.25e-06,
			"cache_read_input_token_cost":     5e-07,
		},
		"gpt-4": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 3e-05,
		},
	}
	body, _ := json.Marshal(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "sub", "pricing.json")

	err := fetchAndFilter(srv.URL, outPath)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling written file: %v", err)
	}
	if _, ok := got["claude-opus-4-6"]; !ok {
		t.Fatal("expected claude-opus-4-6 in output")
	}
	if _, ok := got["gpt-4"]; ok {
		t.Fatal("expected gpt-4 to be filtered out")
	}
}

func TestFetchAndFilter_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "pricing.json")

	err := fetchAndFilter(srv.URL, outPath)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}
