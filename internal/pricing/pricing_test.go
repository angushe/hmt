package pricing

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestLoadFromJSON_SkipsTheSchemaMarker pins the other half of the sentinel
// mechanism. Without the skip the marker becomes a model literally named
// __hmt_cache_schema_v1__, priced at zero.
func TestLoadFromJSON_SkipsTheSchemaMarker(t *testing.T) {
	table, err := LoadFromJSON([]byte(`{"__hmt_cache_schema_v1__":{},"claude-opus-4-6":{"input_cost_per_token":5e-06}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Lookup(cacheSchemaKey); ok {
		t.Errorf("%q was loaded as a model", cacheSchemaKey)
	}
	if _, ok := table.Lookup("claude-opus-4-6"); !ok {
		t.Error("real models must still load alongside the marker")
	}
}

// TestCost covers the function directly; it was previously exercised only
// indirectly through report.ComputeCosts.
func TestCost(t *testing.T) {
	p := ModelPricing{
		InputCostPerToken:      2e-06,
		OutputCostPerToken:     1e-05,
		CacheWriteCostPerToken: 2.5e-06,
		CacheReadCostPerToken:  2e-07,
	}
	// 1000*2e-06 + 100*1e-05 + 500*2.5e-06 + 10000*2e-07 = 0.002+0.001+0.00125+0.002
	if got, want := Cost(p, 1000, 100, 500, 10000), 0.00625; math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
	if got := Cost(p, 0, 0, 0, 0); got != 0 {
		t.Errorf("Cost of nothing = %v, want 0", got)
	}
	// Each column is priced by its own rate, so zeroing one must remove only it.
	if got, want := Cost(p, 1000, 0, 0, 0), 0.002; math.Abs(got-want) > 1e-12 {
		t.Errorf("input-only Cost = %v, want %v", got, want)
	}
}

func TestLookup_NotFound(t *testing.T) {
	table, _ := LoadFromJSON([]byte(`{}`))
	_, ok := table.Lookup("nonexistent")
	if ok {
		t.Fatal("expected ok=false for missing model")
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
		"gemini-pro": map[string]any{
			"litellm_provider":     "vertex_ai",
			"input_cost_per_token": 1e-06,
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
	if _, ok := got["gpt-4"]; !ok {
		t.Fatal("expected gpt-4 (openai) in output")
	}
	if _, ok := got["gemini-pro"]; ok {
		t.Fatal("expected gemini-pro (vertex_ai) to be filtered out")
	}
	if _, ok := got["__hmt_cache_schema_v1__"]; !ok {
		t.Fatal("expected cache schema marker in output")
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

func TestLoad_FreshCache(t *testing.T) {
	tmp := t.TempDir()
	cachedPath := filepath.Join(tmp, "pricing.json")
	cachedData := `{"__hmt_cache_schema_v1__":{},"claude-opus-4-6":{"input_cost_per_token":5e-06,"output_cost_per_token":2.5e-05,"cache_creation_input_token_cost":6.25e-06,"cache_read_input_token_cost":5e-07}}`
	os.WriteFile(cachedPath, []byte(cachedData), 0o644)

	origURL := litellmURL
	litellmURL = "http://127.0.0.1:0/should-not-be-called"
	defer func() { litellmURL = origURL }()

	table, err := Load(cachedPath, 1*time.Hour)
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
}

func TestLoad_FreshLegacyCacheRefreshesForOpenAIModels(t *testing.T) {
	payload := map[string]any{
		"claude-opus-4-6": map[string]any{
			"litellm_provider":     "anthropic",
			"input_cost_per_token": 5e-06,
		},
		"gpt-5.6-sol": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 1.75e-06,
		},
	}
	body, _ := json.Marshal(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cachedPath := filepath.Join(tmp, "pricing.json")
	legacyData := `{"claude-opus-4-6":{"input_cost_per_token":5e-06}}`
	if err := os.WriteFile(cachedPath, []byte(legacyData), 0o644); err != nil {
		t.Fatal(err)
	}

	origURL := litellmURL
	litellmURL = srv.URL
	defer func() { litellmURL = origURL }()

	table, err := Load(cachedPath, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Lookup("gpt-5.6-sol"); !ok {
		t.Fatal("expected fresh legacy cache to refresh and include OpenAI models")
	}
}

func TestLoad_FreshLegacyCacheFetchFailsWarnsAboutMissingOpenAICoverage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cachedPath := filepath.Join(t.TempDir(), "pricing.json")
	legacyData := `{"claude-opus-4-6":{"input_cost_per_token":5e-06}}`
	if err := os.WriteFile(cachedPath, []byte(legacyData), 0o644); err != nil {
		t.Fatal(err)
	}
	origURL := litellmURL
	litellmURL = srv.URL
	t.Cleanup(func() { litellmURL = origURL })

	var (
		table   *Table
		loadErr error
	)
	stderr := capturePricingStderr(t, func() {
		table, loadErr = Load(cachedPath, time.Hour)
	})
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := table.Lookup("claude-opus-4-6"); !ok {
		t.Fatal("expected legacy Anthropic pricing to remain available")
	}
	if !strings.Contains(stderr, "using legacy pricing cache without OpenAI model coverage") {
		t.Errorf("stderr = %q, want legacy-schema coverage warning", stderr)
	}
}

// TestLoad_CorruptStaleCacheReportsBothCauses: when the fetch fails and the
// cache it falls back to is unparseable, the parse error alone leaves no trace
// of the fetch failure that sent the user there.
func TestLoad_CorruptStaleCacheReportsBothCauses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cachedPath := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(cachedPath, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	origURL := litellmURL
	litellmURL = srv.URL
	t.Cleanup(func() { litellmURL = origURL })

	var loadErr error
	capturePricingStderr(t, func() {
		_, loadErr = Load(cachedPath, time.Hour)
	})
	if loadErr == nil {
		t.Fatal("expected an error when both the fetch and the cache fail")
	}
	if !strings.Contains(loadErr.Error(), "parsing pricing JSON") {
		t.Errorf("error = %v, want the parse failure named", loadErr)
	}
	if !strings.Contains(loadErr.Error(), "after fetch failed") {
		t.Errorf("error = %v, want the fetch failure named too", loadErr)
	}
}

func TestLoad_ExpiredCache_FetchSucceeds(t *testing.T) {
	payload := map[string]any{
		"claude-opus-4-6": map[string]any{
			"litellm_provider":                "anthropic",
			"input_cost_per_token":            9.99e-06,
			"output_cost_per_token":           1e-05,
			"cache_creation_input_token_cost": 1e-06,
			"cache_read_input_token_cost":     1e-07,
		},
	}
	body, _ := json.Marshal(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cachedPath := filepath.Join(tmp, "pricing.json")
	os.WriteFile(cachedPath, []byte(`{"old":{}}`), 0o644)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(cachedPath, old, old)

	origURL := litellmURL
	litellmURL = srv.URL
	defer func() { litellmURL = origURL }()

	table, err := Load(cachedPath, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := table.Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("expected claude-opus-4-6 from fresh fetch")
	}
	if p.InputCostPerToken != 9.99e-06 {
		t.Errorf("input cost = %v, want 9.99e-06", p.InputCostPerToken)
	}
}

func TestLoad_ExpiredCache_FetchFails_UsesStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cachedPath := filepath.Join(tmp, "pricing.json")
	staleData := `{"claude-opus-4-6":{"input_cost_per_token":5e-06,"output_cost_per_token":2.5e-05,"cache_creation_input_token_cost":6.25e-06,"cache_read_input_token_cost":5e-07}}`
	os.WriteFile(cachedPath, []byte(staleData), 0o644)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(cachedPath, old, old)

	origURL := litellmURL
	litellmURL = srv.URL
	defer func() { litellmURL = origURL }()

	table, err := Load(cachedPath, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := table.Lookup("claude-opus-4-6")
	if !ok {
		t.Fatal("expected stale cache to still work")
	}
}

func TestLoad_NoCache_FetchFails_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origURL := litellmURL
	litellmURL = srv.URL
	defer func() { litellmURL = origURL }()

	_, err := Load("/nonexistent/pricing.json", 1*time.Hour)
	if err == nil {
		t.Fatal("expected error when no cache and fetch fails")
	}
}

func capturePricingStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	// Restored on return, not via t.Cleanup: leaving a closed pipe as os.Stderr
	// for the rest of the test would silently swallow any later diagnostic.
	defer func() { os.Stderr = old }()

	// Drain concurrently: reading only after fn() returns would deadlock rather
	// than fail once fn() writes past the ~64 KiB pipe buffer.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
