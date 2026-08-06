package parser

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeCodexFile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseCodexFile(path string) ([]Record, error) {
	reader := bufio.NewReaderSize(bytes.NewReader(nil), maxLineBytes)
	result, err := parseCodexFileWithReader(path, path, reader)
	lineageID := resolveCodexLineage(result.lineageID, result.lineageLinks)
	records := make([]Record, len(result.pending))
	for i, p := range result.pending {
		records[i] = p.rec
		records[i].DedupKey = codexDedupKey(lineageID, p.keyPart)
	}
	return records, err
}

const codexMeta = `{"timestamp":"2026-08-04T02:37:01.940Z","type":"session_meta","payload":{"session_id":"sess-1","id":"thread-1","cwd":"/Users/alice/work/proj","cli_version":"0.146.0"}}`

func codexTurnContext(model string) string {
	return `{"timestamp":"2026-08-04T02:37:16.420Z","type":"turn_context","payload":{"turn_id":"t-1","cwd":"/Users/alice/work/proj","model":"` + model + `","effort":"max"}}`
}

func codexUsageJSON(in, cached, cacheWrite, out, reasoning, total int64) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}`, in, cached, cacheWrite, out, reasoning, total)
}

func codexTokenCount(ts, last, total string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` + total + `,"last_token_usage":` + last + `,"model_context_window":258400}}}`
}

func TestParseCodexFile_Basic(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(1000, 600, 25, 50, 10, 1050),
			codexUsageJSON(1000, 600, 25, 50, 10, 1050)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	r := records[0]
	if r.Model != "gpt-5.6-sol" {
		t.Errorf("model = %q, want gpt-5.6-sol", r.Model)
	}
	if r.SessionID != "sess-1" {
		t.Errorf("sessionID = %q, want sess-1", r.SessionID)
	}
	if r.ProjectDir != "/Users/alice/work/proj" {
		t.Errorf("projectDir = %q, want /Users/alice/work/proj", r.ProjectDir)
	}
	// 归一化：input 是未命中缓存部分，cached 归入 cache read
	if r.InputTokens != 400 {
		t.Errorf("input = %d, want 400 (1000-600)", r.InputTokens)
	}
	if r.CacheReadTokens != 600 {
		t.Errorf("cacheRead = %d, want 600", r.CacheReadTokens)
	}
	if r.CacheWriteTokens != 25 {
		t.Errorf("cacheWrite = %d, want 25", r.CacheWriteTokens)
	}
	if r.OutputTokens != 50 {
		t.Errorf("output = %d, want 50", r.OutputTokens)
	}
	if r.DedupKey != "codex:sess-1:1000:600:25:50:10:1050" {
		t.Errorf("dedupKey = %q, want codex:sess-1:1000:600:25:50:10:1050", r.DedupKey)
	}
	want := time.Date(2026, 8, 4, 2, 40, 0, 0, time.UTC)
	if !r.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", r.Timestamp, want)
	}
}

func TestParseCodexFile_OldFormatMissingCacheWrite(t *testing.T) {
	// 0.104 时期的 usage 对象没有 cache_write_input_tokens 字段
	oldUsage := `{"input_tokens":6141,"cached_input_tokens":0,"output_tokens":266,"reasoning_output_tokens":124,"total_tokens":6407}`
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.3-codex"),
		codexTokenCount("2026-02-26T09:05:31.511Z", oldUsage, oldUsage),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].InputTokens != 6141 {
		t.Errorf("input = %d, want 6141", records[0].InputTokens)
	}
	if records[0].CacheWriteTokens != 0 {
		t.Errorf("cacheWrite = %d, want 0", records[0].CacheWriteTokens)
	}
	if records[0].DedupKey != "codex:sess-1:6141:0:0:266:124:6407" {
		t.Errorf("dedupKey = %q, want codex:sess-1:6141:0:0:266:124:6407", records[0].DedupKey)
	}
}

func TestParseCodexFile_SkipsNullInfoLastAndTotal(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		`{"timestamp":"2026-08-04T02:38:00.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex"}}}`,
		`{"timestamp":"2026-08-04T02:38:01.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` + codexUsageJSON(1, 0, 0, 1, 0, 2) + `,"last_token_usage":null}}}`,
		`{"timestamp":"2026-08-04T02:38:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":null,"last_token_usage":` + codexUsageJSON(1, 0, 0, 1, 0, 2) + `}}}`,
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("len = %d, want 0", len(records))
	}
}

func TestParseCodexFile_SkipsInvalidTimestamp(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("not-a-timestamp",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	var (
		records  []Record
		parseErr error
	)
	stderr := captureStderr(t, func() {
		records, parseErr = parseCodexFile(path)
	})
	err := parseErr
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("len = %d, want 0", len(records))
	}
	if !strings.Contains(stderr, "warning: skipping token_count with invalid timestamp in "+path+" at line 3") {
		t.Errorf("stderr = %q, want invalid-timestamp warning", stderr)
	}
}

func TestParseCodexFile_ModelBeforeFirstTurnContext(t *testing.T) {
	// fork 重放行位于首条 turn_context 之前，应回填首条 turn_context 的模型
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTokenCount("2026-08-04T02:37:01.941Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexTurnContext("gpt-5.3-codex"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(200, 0, 0, 20, 0, 220),
			codexUsageJSON(300, 0, 0, 30, 0, 330)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2", len(records))
	}
	if records[0].Model != "gpt-5.3-codex" {
		t.Errorf("backfilled model = %q, want gpt-5.3-codex", records[0].Model)
	}
	if records[1].Model != "gpt-5.3-codex" {
		t.Errorf("model = %q, want gpt-5.3-codex", records[1].Model)
	}
}

func TestParseCodexFile_NoTurnContext(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].Model != "unknown" {
		t.Errorf("model = %q, want unknown", records[0].Model)
	}
}

func TestParseCodexFile_ModelSwitch(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.5"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexTurnContext("codex-auto-review"),
		codexTokenCount("2026-08-04T02:41:00.000Z",
			codexUsageJSON(200, 0, 0, 20, 0, 220),
			codexUsageJSON(300, 0, 0, 30, 0, 330)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2", len(records))
	}
	if records[0].Model != "gpt-5.5" {
		t.Errorf("first model = %q, want gpt-5.5", records[0].Model)
	}
	if records[1].Model != "codex-auto-review" {
		t.Errorf("second model = %q, want codex-auto-review", records[1].Model)
	}
}

func TestParseCodexFile_FirstMetaIsDisplayIdentityExplicitLinkSetsLineageRoot(t *testing.T) {
	child := `{"timestamp":"2026-08-04T02:37:01.940Z","type":"session_meta","payload":{"session_id":"sess-1","id":"thread-1","forked_from_id":"sess-parent","cwd":"/Users/alice/work/proj"}}`
	copied := `{"timestamp":"2026-08-04T02:37:01.941Z","type":"session_meta","payload":{"session_id":"sess-parent","id":"thread-0","cwd":"/Users/alice/other"}}`
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		child,
		copied,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].SessionID != "sess-1" {
		t.Errorf("sessionID = %q, want sess-1 (first meta wins)", records[0].SessionID)
	}
	if records[0].ProjectDir != "/Users/alice/work/proj" {
		t.Errorf("projectDir = %q, want /Users/alice/work/proj", records[0].ProjectDir)
	}
	if records[0].DedupKey != "codex:sess-parent:100:0:0:10:0:110" {
		t.Errorf("dedupKey = %q, want ancestor-root key", records[0].DedupKey)
	}
}

// TestParseCodexFile_MetaAfterTokenCount pins retroactive application: a rollout
// may carry its session_meta after the first token_count, and finalize() has to
// stamp the earlier records too.
func TestParseCodexFile_MetaAfterTokenCount(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexMeta,
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].SessionID != "sess-1" || records[0].ProjectDir != "/Users/alice/work/proj" {
		t.Errorf("record = %+v, want the late meta applied retroactively", records[0])
	}
}

// TestParseCodexFile_TurnContextWithoutModel: an empty model must neither block
// backfill nor reset the current model. The two halves need different fixtures —
// with `model` still empty, falling through is a no-op, so the first case alone
// leaves the guard untested.
func TestParseCodexFile_TurnContextWithoutModel(t *testing.T) {
	const modelless = `{"timestamp":"2026-08-04T02:37:10.000Z","type":"turn_context","payload":{"turn_id":"t-0","cwd":"/Users/alice/work/proj"}}`

	t.Run("does not block backfill", func(t *testing.T) {
		tmp := t.TempDir()
		path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
			codexMeta,
			modelless,
			codexTokenCount("2026-08-04T02:40:00.000Z",
				codexUsageJSON(100, 0, 0, 10, 0, 110),
				codexUsageJSON(100, 0, 0, 10, 0, 110)),
			codexTurnContext("gpt-5.6-sol"),
		})
		records, err := parseCodexFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 || records[0].Model != "gpt-5.6-sol" {
			t.Fatalf("records = %+v, want backfill to survive the model-less turn_context", records)
		}
	})

	// With a model already established, dropping the guard attributes the second
	// record to "unknown" — and a later real turn_context would then re-enter the
	// backfill branch and overwrite the already-correct first record.
	t.Run("does not reset an established model", func(t *testing.T) {
		tmp := t.TempDir()
		path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
			codexMeta,
			codexTurnContext("gpt-5.6-sol"),
			codexTokenCount("2026-08-04T02:40:00.000Z",
				codexUsageJSON(100, 0, 0, 10, 0, 110),
				codexUsageJSON(100, 0, 0, 10, 0, 110)),
			modelless,
			codexTokenCount("2026-08-04T02:41:00.000Z",
				codexUsageJSON(200, 0, 0, 20, 0, 220),
				codexUsageJSON(300, 0, 0, 30, 0, 330)),
		})
		records, err := parseCodexFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 2 {
			t.Fatalf("len = %d, want 2", len(records))
		}
		for i, r := range records {
			if r.Model != "gpt-5.6-sol" {
				t.Errorf("record %d model = %q, want gpt-5.6-sol", i, r.Model)
			}
		}
	})
}

// TestParseCodexFile_MissingMetaGetsAProjectFallback: SessionID already had a
// fallback; ProjectDir did not, leaving a blank PROJECT cell no --project value
// could select.
func TestParseCodexFile_MissingMetaGetsAProjectFallback(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].ProjectDir != "unknown" {
		t.Errorf("projectDir = %q, want the same fallback the model field uses", records[0].ProjectDir)
	}
	if !strings.HasPrefix(records[0].SessionID, "missing-meta-") {
		t.Errorf("sessionID = %q, want the missing-meta fallback", records[0].SessionID)
	}
}

// TestParseCodexFile_ClampsCachedInputToInput: a malformed cache figure must not
// let CacheRead exceed the source's own input_tokens, which would report more
// total tokens than exist and overstate cost.
func TestParseCodexFile_ClampsCachedInputToInput(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 400, 0, 10, 0, 110),
			codexUsageJSON(100, 400, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := records[0]
	if r.InputTokens != 0 {
		t.Errorf("input = %d, want 0 (floored)", r.InputTokens)
	}
	if r.CacheReadTokens != 100 {
		t.Errorf("cacheRead = %d, want 100 (clamped to input, not the bogus 400)", r.CacheReadTokens)
	}
	if got := r.InputTokens + r.CacheReadTokens; got != 100 {
		t.Errorf("input+cacheRead = %d, want to equal the source input_tokens 100", got)
	}
}

// TestLineageLinks_LastDeclarationWins pins both halves of one rule. Within a
// file a later session_meta overwrites an earlier link for the same child;
// across files the later-walked file wins. Two opposite resolutions for one map
// is what a future edit gets silently wrong, so both directions are asserted.
func TestLineageLinks_LastDeclarationWins(t *testing.T) {
	meta := func(session, id, forkedFrom string) string {
		return fmt.Sprintf(`{"timestamp":"2026-08-04T02:37:00.000Z","type":"session_meta","payload":{"session_id":%q,"id":%q,"forked_from_id":%q,"cwd":"/Users/alice/work/proj"}}`,
			session, id, forkedFrom)
	}
	// One usage record whose dedup key exposes which root the child resolved to.
	usage := codexTokenCount("2026-08-04T02:40:00.000Z",
		codexUsageJSON(100, 0, 0, 10, 0, 110),
		codexUsageJSON(100, 0, 0, 10, 0, 110))

	t.Run("across files, the later walk order wins", func(t *testing.T) {
		tmp := t.TempDir()
		// WalkDir is lexical, so rollout-b is read after rollout-a.
		writeCodexFile(t, tmp, "rollout-a.jsonl", []string{meta("child", "child", "parent-a"), codexTurnContext("gpt-5.6-sol"), usage})
		writeCodexFile(t, tmp, "rollout-b.jsonl", []string{meta("child", "child", "parent-b")})

		records, err := ScanCodexDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 {
			t.Fatalf("len = %d, want 1", len(records))
		}
		if want := codexDedupKey("parent-b", "100:0:0:10:0:110"); records[0].DedupKey != want {
			t.Errorf("dedupKey = %q, want %q — the last file's parent must win",
				records[0].DedupKey, want)
		}
	})

	t.Run("within a file, the later session_meta wins", func(t *testing.T) {
		tmp := t.TempDir()
		writeCodexFile(t, tmp, "rollout-one.jsonl", []string{
			meta("child", "child", "parent-first"),
			meta("child", "child", "parent-second"),
			codexTurnContext("gpt-5.6-sol"),
			usage,
		})
		records, err := ScanCodexDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 {
			t.Fatalf("len = %d, want 1", len(records))
		}
		if want := codexDedupKey("parent-second", "100:0:0:10:0:110"); records[0].DedupKey != want {
			t.Errorf("dedupKey = %q, want %q — the later meta's parent must win",
				records[0].DedupKey, want)
		}
	})
}

func TestParseCodexFile_DoesNotInferLineageFromMetaPosition(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		`{"timestamp":"2026-08-04T02:37:01Z","type":"session_meta","payload":{"session_id":"child","id":"child","cwd":"/Users/alice/work/proj"}}`,
		`{"timestamp":"2026-08-04T02:37:02Z","type":"session_meta","payload":{"session_id":"parent","id":"parent","cwd":"/Users/alice/work/proj"}}`,
	})
	reader := bufio.NewReaderSize(bytes.NewReader(nil), maxLineBytes)
	result, err := parseCodexFileWithReader(path, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if result.lineageID != "child" {
		t.Errorf("lineageID = %q, want first meta identity child", result.lineageID)
	}
	if _, inferred := result.lineageLinks["child"]; inferred {
		t.Errorf("lineage links = %v, positional child→parent link must not be inferred", result.lineageLinks)
	}
}

func TestParseCodexFile_SingleMetaUsesLineageLinkForDedup(t *testing.T) {
	meta := `{"timestamp":"2026-08-04T02:37:01.940Z","type":"session_meta","payload":{"session_id":"sess-child","id":"thread-child","forked_from_id":"sess-root","cwd":"/Users/alice/work/proj"}}`
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		meta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].SessionID != "sess-child" {
		t.Errorf("sessionID = %q, want child display identity", records[0].SessionID)
	}
	if records[0].DedupKey != "codex:sess-root:100:0:0:10:0:110" {
		t.Errorf("dedupKey = %q, want linked lineage root", records[0].DedupKey)
	}
}

func TestParseCodexFile_SessionIDFallsBackToPayloadID(t *testing.T) {
	metaWithoutSessionID := `{"timestamp":"2026-08-04T02:37:01.940Z","type":"session_meta","payload":{"id":"thread-1","cwd":"/Users/alice/work/proj"}}`
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		metaWithoutSessionID,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].SessionID != "thread-1" {
		t.Errorf("sessionID = %q, want thread-1", records[0].SessionID)
	}
	if records[0].DedupKey != "codex:thread-1:100:0:0:10:0:110" {
		t.Errorf("dedupKey = %q, want codex:thread-1:100:0:0:10:0:110", records[0].DedupKey)
	}
}

func TestParseCodexFile_NoSessionMetaUsesPrivateFileIdentifier(t *testing.T) {
	tmp := t.TempDir()
	lines := []string{
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	}
	firstPath := writeCodexFile(t, tmp, "first.jsonl", lines)
	secondPath := writeCodexFile(t, tmp, "second.jsonl", lines)
	first, err := parseCodexFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseCodexFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := parseCodexFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].SessionID != firstAgain[0].SessionID {
		t.Fatalf("session IDs = %q, %q; want deterministic fallback", first[0].SessionID, firstAgain[0].SessionID)
	}
	if first[0].SessionID == second[0].SessionID || first[0].DedupKey == second[0].DedupKey {
		t.Fatalf("identifiers should isolate files: first=%q/%q second=%q/%q",
			first[0].SessionID, first[0].DedupKey, second[0].SessionID, second[0].DedupKey)
	}
	for _, record := range []Record{first[0], second[0]} {
		if filepath.IsAbs(record.SessionID) || strings.Contains(record.SessionID, tmp) || strings.Contains(record.DedupKey, tmp) {
			t.Errorf("public identifiers expose absolute path: session=%q dedup=%q", record.SessionID, record.DedupKey)
		}
	}
	if got := Dedup(append(first, second...)); len(got) != 2 {
		t.Fatalf("deduped len = %d, want 2", len(got))
	}
}

func TestParseCodexFile_SyntheticFillEvent(t *testing.T) {
	// 上下文占满时的合成计数：last 仅 total_tokens 非零，映射后为零用量记录
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(0, 0, 0, 0, 0, 258400),
			codexUsageJSON(0, 0, 0, 0, 0, 258400)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	r := records[0]
	if r.InputTokens != 0 || r.OutputTokens != 0 || r.CacheReadTokens != 0 || r.CacheWriteTokens != 0 {
		t.Errorf("synthetic fill event should map to zero usage, got %+v", r)
	}
}

func TestParseCodexFile_CachedInputGreaterThanInputSaturatesAtZero(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 200, 0, 10, 0, 110),
			codexUsageJSON(100, 200, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].InputTokens != 0 {
		t.Errorf("input = %d, want saturating floor 0", records[0].InputTokens)
	}
}

func TestParseCodexFile_IgnoresOtherLines(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout.jsonl", []string{
		codexMeta,
		// 大体积 response_item 行，内容里恰好含预筛标记词（超集过滤的误命中）
		`{"timestamp":"2026-08-04T02:39:00.000Z","type":"response_item","payload":{"type":"message","content":"discussing \"token_count\" and \"turn_context\" here"}}`,
		`{"type":"event_msg","payload":{"type":"token_count"`,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].Model != "gpt-5.6-sol" {
		t.Errorf("model = %q, want gpt-5.6-sol", records[0].Model)
	}
}

func TestScanCodexDir_SkipsOversizedLineAndKeepsSurroundingUsage(t *testing.T) {
	tmp := t.TempDir()
	path := writeCodexFile(t, tmp, "rollout-oversized.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		`{"timestamp":"2026-08-04T02:40:30.000Z","type":"response_item","payload":{"content":"` + strings.Repeat("x", 10*1024*1024+1) + `"}}`,
		codexTokenCount("2026-08-04T02:41:00.000Z",
			codexUsageJSON(200, 0, 0, 20, 0, 220),
			codexUsageJSON(300, 0, 0, 30, 0, 330)),
	})
	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanCodexDir(tmp)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2 (usage before and after oversized line)", len(records))
	}
	if records[0].InputTokens != 100 || records[1].InputTokens != 200 {
		t.Errorf("input tokens = %d, %d; want 100, 200", records[0].InputTokens, records[1].InputTokens)
	}
	// The warning names the path as configured, not as symlink-resolved.
	if !strings.Contains(stderr, "warning: skipping oversized line in "+path) {
		t.Errorf("stderr = %q, want contextual oversized-line warning", stderr)
	}
}

func captureStderr(t *testing.T, fn func()) string {
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

type codexErrorReader struct{}

func (codexErrorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("forced read failure")
}

func TestParseCodexReader_ReturnsFinalizedRecordsWithContextualReadError(t *testing.T) {
	path := "/private/session/rollout-error.jsonl"
	data := strings.Join([]string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	}, "\n") + "\n"

	result, err := parseCodexBufferedReader(
		bufio.NewReaderSize(io.MultiReader(strings.NewReader(data), codexErrorReader{}), maxLineBytes),
		path,
	)
	if err == nil || !strings.Contains(err.Error(), "reading "+path+": forced read failure") {
		t.Fatalf("error = %v, want contextual read failure", err)
	}
	if len(result.pending) != 1 {
		t.Fatalf("len = %d, want 1 finalized record", len(result.pending))
	}
	rec := result.pending[0].rec
	if rec.SessionID != "sess-1" || rec.ProjectDir != "/Users/alice/work/proj" {
		t.Errorf("record identity = %+v, want parsed session and project", rec)
	}
	if result.lineageID != "sess-1" || result.pending[0].keyPart != "100:0:0:10:0:110" {
		t.Errorf("lineage/key part = %q/%q, want parsed dedup components", result.lineageID, result.pending[0].keyPart)
	}
}

// TestScanCodexDir_ReplayDedupAcrossForkAndResume covers both rollout shapes
// that replay a parent's token_count lines: a fork keeps the parent's
// session_id, while a resume takes a new one and links back through
// forked_from_id. Both must resolve to the same lineage, so the replayed lines
// dedup against the parent's originals rather than double-counting.
func TestScanCodexDir_ReplayDedupAcrossForkAndResume(t *testing.T) {
	for _, tc := range []struct {
		name      string
		childMeta []string
	}{
		{"fork", []string{`{"timestamp":"2026-08-04T02:45:00.000Z","type":"session_meta","payload":{"session_id":"sess-1","id":"thread-child","parent_thread_id":"sess-1","cwd":"/Users/alice/work/proj"}}`}},
		{"resume", []string{
			`{"timestamp":"2026-08-04T02:45:00.000Z","type":"session_meta","payload":{"session_id":"sess-child","id":"thread-child","forked_from_id":"sess-1","cwd":"/Users/alice/work/proj"}}`,
			codexMeta,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			// 父线程文件：两次真实请求
			writeCodexFile(t, tmp, "2026/08/04/rollout-2026-08-04T10-00-00-parent.jsonl", []string{
				codexMeta,
				codexTurnContext("gpt-5.6-sol"),
				codexTokenCount("2026-08-04T02:40:00.000Z",
					codexUsageJSON(100, 0, 0, 10, 0, 110),
					codexUsageJSON(100, 0, 0, 10, 0, 110)),
				codexTokenCount("2026-08-04T02:41:00.000Z",
					codexUsageJSON(200, 0, 0, 20, 0, 220),
					codexUsageJSON(300, 0, 0, 30, 0, 330)),
			})
			// 子线程文件：开头重放父线程两条计数（时间戳已被改写），随后一次真实请求
			lines := append([]string{}, tc.childMeta...)
			lines = append(lines,
				codexTokenCount("2026-08-04T02:45:00.001Z",
					codexUsageJSON(100, 0, 0, 10, 0, 110),
					codexUsageJSON(100, 0, 0, 10, 0, 110)),
				codexTokenCount("2026-08-04T02:45:00.001Z",
					codexUsageJSON(200, 0, 0, 20, 0, 220),
					codexUsageJSON(300, 0, 0, 30, 0, 330)),
				codexTurnContext("gpt-5.6-sol"),
				codexTokenCount("2026-08-04T02:46:00.000Z",
					codexUsageJSON(400, 0, 0, 40, 0, 440),
					codexUsageJSON(700, 0, 0, 70, 0, 770)),
			)
			writeCodexFile(t, tmp, "2026/08/04/rollout-2026-08-04T10-45-00-child.jsonl", lines)

			records, err := ScanCodexDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 3 {
				t.Fatalf("len = %d, want 3 (2 real parent + 1 real child, replays deduped)", len(records))
			}
			var totalInput int64
			for _, r := range records {
				totalInput += r.InputTokens
			}
			if totalInput != 700 {
				t.Errorf("total input = %d, want 700 (100+200+400)", totalInput)
			}
			// 先见先留：保留父文件的原始时间戳（02:40），而非重放行的 02:45
			want := time.Date(2026, 8, 4, 2, 40, 0, 0, time.UTC)
			found := false
			for _, r := range records {
				if r.Timestamp.Equal(want) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected original parent timestamp %v to survive dedup", want)
			}
		})
	}
}

func TestScanCodexDir_ResolvesMultiHopLineage(t *testing.T) {
	tmp := t.TempDir()
	rootMeta := `{"timestamp":"2026-08-04T02:40:00.000Z","type":"session_meta","payload":{"session_id":"root","id":"root","cwd":"/Users/alice/work/proj"}}`
	childMeta := `{"timestamp":"2026-08-04T02:45:00.000Z","type":"session_meta","payload":{"session_id":"child","id":"child","forked_from_id":"root","cwd":"/Users/alice/work/proj"}}`
	grandchildMeta := `{"timestamp":"2026-08-04T02:50:00.000Z","type":"session_meta","payload":{"session_id":"child","id":"grandchild","forked_from_id":"child","cwd":"/Users/alice/work/proj"}}`

	writeCodexFile(t, tmp, "rollout-root.jsonl", []string{
		rootMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:41:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	writeCodexFile(t, tmp, "rollout-child.jsonl", []string{
		childMeta,
		rootMeta,
		codexTokenCount("2026-08-04T02:45:00.001Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:46:00.000Z",
			codexUsageJSON(200, 0, 0, 20, 0, 220),
			codexUsageJSON(300, 0, 0, 30, 0, 330)),
	})
	writeCodexFile(t, tmp, "rollout-grandchild.jsonl", []string{
		grandchildMeta,
		codexTokenCount("2026-08-04T02:50:00.001Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexTokenCount("2026-08-04T02:50:00.002Z",
			codexUsageJSON(200, 0, 0, 20, 0, 220),
			codexUsageJSON(300, 0, 0, 30, 0, 330)),
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:51:00.000Z",
			codexUsageJSON(300, 0, 0, 30, 0, 330),
			codexUsageJSON(600, 0, 0, 60, 0, 660)),
	})

	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("len = %d, want 3 unique requests across multi-hop lineage", len(records))
	}
	var input int64
	for _, record := range records {
		input += record.InputTokens
	}
	if input != 600 {
		t.Errorf("input = %d, want 600", input)
	}
}

func TestResolveCodexLineage_MultiHopAndCycle(t *testing.T) {
	tests := []struct {
		name  string
		start string
		links map[string]string
		want  string
	}{
		{"multi-hop", "grandchild", map[string]string{"grandchild": "child", "child": "root"}, "root"},
		{"cycle-from-a", "a", map[string]string{"a": "b", "b": "a"}, "a"},
		{"cycle-from-b", "b", map[string]string{"a": "b", "b": "a"}, "a"},
		{"cycle-with-tail", "aaa", map[string]string{"aaa": "yyy", "yyy": "zzz", "zzz": "yyy"}, "yyy"},
		{"same-cycle-without-tail", "yyy", map[string]string{"aaa": "yyy", "yyy": "zzz", "zzz": "yyy"}, "yyy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveCodexLineage(tt.start, tt.links); got != tt.want {
				t.Errorf("resolveCodexLineage(%q) = %q, want %q", tt.start, got, tt.want)
			}
		})
	}
}

func TestScanCodexDir_SubagentSiblingsShareDisplaySessionButDedupSeparately(t *testing.T) {
	tmp := t.TempDir()
	usage := codexUsageJSON(100, 0, 0, 10, 0, 110)
	for _, tc := range []struct {
		name     string
		threadID string
	}{
		{"rollout-sibling-a.jsonl", "sibling-a"},
		{"rollout-sibling-b.jsonl", "sibling-b"},
	} {
		meta := fmt.Sprintf(`{"timestamp":"2026-08-04T02:40:00.000Z","type":"session_meta","payload":{"session_id":"parent","id":%q,"parent_thread_id":"parent","thread_source":"subagent","cwd":"/Users/alice/work/proj"}}`, tc.threadID)
		writeCodexFile(t, tmp, tc.name, []string{
			meta,
			codexTurnContext("gpt-5.6-sol"),
			codexTokenCount("2026-08-04T02:41:00.000Z", usage, usage),
		})
	}

	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2 distinct sibling requests", len(records))
	}
	for _, record := range records {
		if record.SessionID != "parent" {
			t.Errorf("sessionID = %q, want shared display session parent", record.SessionID)
		}
	}
}

func TestScanCodexDir_ForkReplayKeepsEarliestTimestampRegardlessOfPathOrder(t *testing.T) {
	tmp := t.TempDir()
	usage := codexUsageJSON(100, 0, 0, 10, 0, 110)
	// The replay sorts first by path but carries a later rewritten timestamp.
	writeCodexFile(t, tmp, "rollout-a-child.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:45:00.000Z", usage, usage),
	})
	writeCodexFile(t, tmp, "rollout-z-parent.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z", usage, usage),
	})

	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 deduplicated record", len(records))
	}
	want := time.Date(2026, 8, 4, 2, 40, 0, 0, time.UTC)
	if !records[0].Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want earliest original timestamp %v", records[0].Timestamp, want)
	}
}

func TestScanCodexDir_EqualTimestampPrefersNonzeroUsage(t *testing.T) {
	tmp := t.TempDir()
	total := codexUsageJSON(300, 0, 0, 30, 0, 330)
	for _, tc := range []struct {
		name string
		last string
	}{
		{"rollout-a-fill.jsonl", codexUsageJSON(0, 0, 0, 0, 0, 330)},
		{"rollout-z-second.jsonl", codexUsageJSON(200, 0, 0, 20, 0, 220)},
	} {
		writeCodexFile(t, tmp, tc.name, []string{
			codexMeta,
			codexTurnContext("gpt-5.6-sol"),
			codexTokenCount("2026-08-04T02:40:00.000Z", tc.last, total),
		})
	}

	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 deduplicated record", len(records))
	}
	if records[0].InputTokens != 200 {
		t.Errorf("input tokens = %d, want nonzero record's 200", records[0].InputTokens)
	}
}

func TestScanCodexDir_RateLimitReemitDedup(t *testing.T) {
	// 限流刷新会在同一文件内重发 info 未变的 token_count
	tmp := t.TempDir()
	writeCodexFile(t, tmp, "2026/08/04/rollout-a.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
		codexTokenCount("2026-08-04T02:40:30.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 (re-emission deduped)", len(records))
	}
}

func TestScanCodexDir_IgnoresNonRolloutJSONL(t *testing.T) {
	tmp := t.TempDir()
	writeCodexFile(t, tmp, "2026/08/04/unrelated.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	records, err := ScanCodexDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("len = %d, want 0 (non-rollout JSONL ignored)", len(records))
	}
}

func TestScanCodexDir_SkipsZstWithWarning(t *testing.T) {
	tmp := t.TempDir()
	writeCodexFile(t, tmp, "2026/08/04/rollout-a.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	// 压缩文件内容无关紧要，只要不被当作 JSONL 解析即可
	zst := filepath.Join(tmp, "2026", "08", "04", "rollout-b.jsonl.zst")
	if err := os.WriteFile(zst, []byte{0x28, 0xb5, 0x2f, 0xfd}, 0o644); err != nil {
		t.Fatal(err)
	}
	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanCodexDir(tmp)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 (zst skipped)", len(records))
	}
	if !strings.Contains(stderr, "warning: skipping compressed rollout "+zst+" (not counted)") {
		t.Errorf("stderr = %q, want compressed rollout warning", stderr)
	}
}

func TestScanCodexDir_FollowsSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-sessions")
	writeCodexFile(t, realDir, "2026/08/04/rollout-a.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	linkedDir := filepath.Join(tmp, "sessions")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}

	records, err := ScanCodexDir(linkedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 from symlinked root", len(records))
	}
}

func TestScanCodexDir_WarnsWhenSkippingNestedSymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target")
	writeCodexFile(t, target, "rollout-a.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})
	root := filepath.Join(tmp, "sessions")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedYear := filepath.Join(root, "2026")
	if err := os.Symlink(target, linkedYear); err != nil {
		t.Fatal(err)
	}

	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanCodexDir(root)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 0 {
		t.Fatalf("len = %d, want 0 because nested directory symlinks are not followed", len(records))
	}
	linkPath := filepath.Join(root, filepath.Base(linkedYear))
	if !strings.Contains(stderr, "warning: skipping symlinked directory "+linkPath) {
		t.Errorf("stderr = %q, want skipped symlink warning", stderr)
	}
}

func TestScanCodexDir_WarnsWhenRolloutsContainNoTokenEvents(t *testing.T) {
	driftLines := []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		`{"timestamp":"2026-08-04T02:40:00.000Z","type":"event_msg","payload":{"type":"usage_count","info":{}}}`,
	}

	scan := func(t *testing.T, files int) (int, string) {
		t.Helper()
		tmp := t.TempDir()
		for i := range files {
			writeCodexFile(t, tmp, fmt.Sprintf("rollout-schema-drift-%d.jsonl", i), driftLines)
		}
		var (
			records []Record
			scanErr error
		)
		stderr := captureStderr(t, func() {
			records, scanErr = ScanCodexDir(tmp)
		})
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		return len(records), stderr
	}

	// Literal counts, not codexSchemaWarnThreshold: referencing the constant
	// would move these expectations with it and pin nothing.
	t.Run("warns once enough files are empty", func(t *testing.T) {
		got, stderr := scan(t, 3)
		if got != 0 {
			t.Fatalf("len = %d, want 0", got)
		}
		want := "warning: found 3 Codex rollout files but no token_count events; either no session completed a turn, or the log schema has changed"
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want schema-drift warning", stderr)
		}
	})

	// Unreadable files say nothing about the schema. Counting them turned a
	// permissions problem into a confident misdiagnosis, printed directly after
	// the open failures that actually explain it.
	t.Run("unreadable files do not count toward schema drift", func(t *testing.T) {
		tmp := t.TempDir()
		for i := range 3 {
			p := writeCodexFile(t, tmp, fmt.Sprintf("rollout-unreadable-%d.jsonl", i), driftLines)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
			if f, err := os.Open(p); err == nil {
				_ = f.Close()
				t.Skip("filesystem permissions do not make the fixture unreadable")
			}
		}
		var scanErr error
		stderr := captureStderr(t, func() {
			_, scanErr = ScanCodexDir(tmp)
		})
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if !strings.Contains(stderr, "warning: opening ") {
			t.Errorf("stderr = %q, want the open failures reported", stderr)
		}
		if strings.Contains(stderr, "log schema has changed") {
			t.Errorf("stderr = %q, want no schema diagnosis for a permissions problem", stderr)
		}
	})

	// A single aborted session — a session_meta with no completed turn — is far
	// likelier than schema drift, and warning about it would fire on every run.
	t.Run("silent for a lone aborted session", func(t *testing.T) {
		got, stderr := scan(t, 1)
		if got != 0 {
			t.Fatalf("len = %d, want 0", got)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want silence for a single empty rollout", stderr)
		}
	})
}

func TestScanCodexDir_WarnsForUnreadableNestedFileAndKeepsSibling(t *testing.T) {
	tmp := t.TempDir()
	badPath := writeCodexFile(t, tmp, "rollout-a-unreadable.jsonl", []string{codexMeta})
	if err := os.Chmod(badPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(badPath, 0o644) })
	if f, err := os.Open(badPath); err == nil {
		_ = f.Close()
		t.Skip("filesystem permissions do not make the fixture unreadable")
	}
	writeCodexFile(t, tmp, "rollout-b-readable.jsonl", []string{
		codexMeta,
		codexTurnContext("gpt-5.6-sol"),
		codexTokenCount("2026-08-04T02:40:00.000Z",
			codexUsageJSON(100, 0, 0, 10, 0, 110),
			codexUsageJSON(100, 0, 0, 10, 0, 110)),
	})

	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanCodexDir(tmp)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want readable sibling record", len(records))
	}
	if !strings.Contains(stderr, "warning: opening "+badPath) {
		t.Errorf("stderr = %q, want unreadable file warning", stderr)
	}
}

func TestScanCodexDir_ReturnsErrorForMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := ScanCodexDir(missing)
	if err == nil || !strings.Contains(err.Error(), "resolving "+missing) {
		t.Fatalf("error = %v, want contextual root resolution error", err)
	}
}
