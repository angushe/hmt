package parser

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// codexUsage matches the TokenUsage object in Codex rollout logs.
// cache_write_input_tokens is absent in older versions and defaults to 0.
type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// codexLine is the subset of a Codex rollout JSONL line we care about.
// The payload fields overlap across the three line types we read; the
// line's Type field decides which of them are meaningful.
type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		// session_meta
		SessionID    string `json:"session_id"`
		ID           string `json:"id"`
		Cwd          string `json:"cwd"`
		ForkedFromID string `json:"forked_from_id"`
		ThreadSource string `json:"thread_source"`
		// turn_context
		Model string `json:"model"`
		// event_msg
		Type string `json:"type"`
		Info *struct {
			TotalTokenUsage *codexUsage `json:"total_token_usage"`
			LastTokenUsage  *codexUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexParseResult struct {
	// pending pairs each record with its dedup key part, so the two cannot fall
	// out of alignment across the producer boundary the way parallel slices did.
	pending      []codexPending
	lineageID    string
	lineageLinks map[string]string
}

// codexMarkers gates full JSON parsing: most rollout lines are large
// response_item entries we do not need. A line containing none of these
// markers cannot be one of the three types we read. False positives are
// fine — the type switch below rejects them.
var codexMarkers = [][]byte{
	[]byte(`"session_meta"`),
	[]byte(`"turn_context"`),
	[]byte(`"token_count"`),
}

func codexLineOfInterest(data []byte) bool {
	for _, m := range codexMarkers {
		if bytes.Contains(data, m) {
			return true
		}
	}
	return false
}

// parseCodexFileWithReader opens path and reports problems against reportPath,
// which is the same location expressed the way the user configured it — the two
// differ when the source root is a symlink.
func parseCodexFileWithReader(path, reportPath string, reader *bufio.Reader) (codexParseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return codexParseResult{}, fmt.Errorf("opening %s: %w", reportPath, err)
	}
	defer f.Close()
	reader.Reset(f)
	return parseCodexBufferedReader(reader, reportPath)
}

// parseCodexBufferedReader parses one Codex rollout file into records, one per
// token_count event (last_token_usage delta). The first session_meta provides
// display identity and lineage identity, while explicit fork links identify
// replay ancestry. Each token_count uses the most recent turn_context,
// with earlier records backfilled from the first model ("unknown" if absent).
// Cached input is split from non-cached input, and cumulative total_token_usage
// provides the replay/rate-limit dedup key suffix.
func parseCodexBufferedReader(reader *bufio.Reader, path string) (codexParseResult, error) {
	var (
		pending      []codexPending
		sessionID    string
		lineageID    string
		lineageLinks = make(map[string]string)
		projDir      string
		metaSeen     bool
		model        string
	)
	finalize := func() codexParseResult {
		if sessionID == "" {
			// Keep no-meta records file-local without exposing the rollout path.
			pathHash := sha256.Sum256([]byte(filepath.Clean(path)))
			sessionID = fmt.Sprintf("missing-meta-%x", pathHash[:8])
		}
		if lineageID == "" {
			lineageID = sessionID
		}
		if projDir == "" {
			// Matches the model field's fallback. Left empty, the row shows a
			// blank PROJECT cell and no --project value can ever select it.
			// Accepted collision: this shares a display name with a real path
			// ending in "unknown", and `--project unknown` substring-matches any
			// path containing it. Every alternative reads worse to a user, and no
			// rollout in the real corpus lacks a cwd.
			projDir = "unknown"
		}
		for i := range pending {
			if pending[i].rec.Model == "" {
				pending[i].rec.Model = "unknown"
			}
			pending[i].rec.SessionID = sessionID
			pending[i].rec.ProjectDir = projDir
		}
		return codexParseResult{
			pending:      pending,
			lineageID:    lineageID,
			lineageLinks: lineageLinks,
		}
	}

	readErr := forEachLine(reader, path, func(data []byte, lineNumber int) {
		if !codexLineOfInterest(data) {
			return
		}
		var cl codexLine
		if err := json.Unmarshal(data, &cl); err != nil {
			return
		}
		switch cl.Type {
		case "session_meta":
			metaID := cl.Payload.SessionID
			if cl.Payload.ThreadSource == "subagent" && cl.Payload.ID != "" {
				metaID = cl.Payload.ID
			} else if metaID == "" {
				metaID = cl.Payload.ID
			}
			if !metaSeen {
				metaSeen = true
				sessionID = cl.Payload.SessionID
				if sessionID == "" {
					sessionID = cl.Payload.ID
				}
				lineageID = metaID
				projDir = cl.Payload.Cwd
			}
			if cl.Payload.ForkedFromID != "" && cl.Payload.ForkedFromID != metaID {
				lineageLinks[metaID] = cl.Payload.ForkedFromID
			}
		case "turn_context":
			if cl.Payload.Model == "" {
				return
			}
			if model == "" {
				for i := range pending {
					pending[i].rec.Model = cl.Payload.Model
				}
			}
			model = cl.Payload.Model
		case "event_msg":
			if cl.Payload.Type != "token_count" || cl.Payload.Info == nil {
				return
			}
			last, total := cl.Payload.Info.LastTokenUsage, cl.Payload.Info.TotalTokenUsage
			if last == nil || total == nil {
				return
			}
			ts, err := time.Parse(time.RFC3339Nano, cl.Timestamp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping token_count with invalid timestamp in %s at line %d: %q\n", path, lineNumber, cl.Timestamp)
				return
			}
			// Clamp both sides together: reporting more cache reads than the
			// source's own input_tokens would overstate the total and the cost.
			cachedInput := last.CachedInputTokens
			if cachedInput > last.InputTokens {
				cachedInput = last.InputTokens
			}
			pending = append(pending, codexPending{
				rec: Record{
					Model:            model,
					Timestamp:        ts,
					InputTokens:      last.InputTokens - cachedInput,
					OutputTokens:     last.OutputTokens,
					CacheWriteTokens: last.CacheWriteInputTokens,
					CacheReadTokens:  cachedInput,
				},
				keyPart: fmt.Sprintf("%d:%d:%d:%d:%d:%d",
					total.InputTokens, total.CachedInputTokens, total.CacheWriteInputTokens,
					total.OutputTokens, total.ReasoningOutputTokens, total.TotalTokens),
			})
		}
	})

	return finalize(), readErr
}

// codexSchemaWarnThreshold is the number of rollout files that must yield no
// token_count events before schema drift is a likelier explanation than a few
// sessions having been aborted mid-turn.
const codexSchemaWarnThreshold = 3

// codexPending is a record awaiting its final dedup key. rec and keyPart are
// filled by the per-file parse; lineage is a per-file constant stamped on by
// ScanCodexDir once the walk knows it, so it is empty on the parse result.
// Previously three slices kept aligned by hand and consumed by index; one struct
// makes misalignment unrepresentable.
type codexPending struct {
	rec     Record
	keyPart string
	lineage string
}

// ScanCodexDir recursively reads all rollout .jsonl files under baseDir,
// parses them, and returns deduplicated records. Compressed .jsonl.zst
// rollouts are skipped with a warning.
func ScanCodexDir(baseDir string) ([]Record, error) {
	resolvedBaseDir, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", baseDir, err)
	}

	var (
		all          []codexPending
		lineageLinks = make(map[string]string)
		rolloutFiles int
	)
	// Walking the resolved root is what makes nested-symlink detection reliable,
	// but the user configured baseDir, so warnings name paths under that.
	display := func(p string) string {
		rel, relErr := filepath.Rel(resolvedBaseDir, p)
		if relErr != nil {
			return p
		}
		return filepath.Join(baseDir, rel)
	}
	reader := bufio.NewReaderSize(bytes.NewReader(nil), maxLineBytes)
	err = filepath.WalkDir(resolvedBaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == resolvedBaseDir {
				return err
			}
			fmt.Fprintf(os.Stderr, "warning: unable to access %s: %v\n", display(path), err)
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping inaccessible symlink %s: %v\n", display(path), statErr)
				return nil
			}
			if info.IsDir() {
				fmt.Fprintf(os.Stderr, "warning: skipping symlinked directory %s (not scanned)\n", display(path))
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if !strings.HasPrefix(name, "rollout-") {
			return nil
		}
		if strings.HasSuffix(name, ".jsonl.zst") {
			fmt.Fprintf(os.Stderr, "warning: skipping compressed rollout %s (not counted)\n", display(path))
			return nil
		}
		if !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		parsed, parseErr := parseCodexFileWithReader(path, display(path), reader)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", parseErr)
		} else {
			// Counted only when the file was actually read: a permission error
			// would otherwise be diagnosed as a schema change below.
			rolloutFiles++
		}
		for _, p := range parsed.pending {
			p.lineage = parsed.lineageID
			all = append(all, p)
		}
		// Last-wins, matching the within-file rule at the session_meta case: one
		// map with two opposite conflict resolutions is what a later edit gets
		// silently wrong. No child declares conflicting parents in the real
		// corpus (0 of 45 links), so this is about the rule, not the data.
		maps.Copy(lineageLinks, parsed.lineageLinks)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", baseDir, err)
	}
	// Below the threshold, an aborted session (a session_meta and no completed
	// turn) is the far likelier explanation than schema drift, and warning about
	// it would fire on every run.
	if rolloutFiles >= codexSchemaWarnThreshold && len(all) == 0 {
		fmt.Fprintf(os.Stderr, "warning: found %d Codex rollout files but no token_count events; either no session completed a turn, or the log schema has changed\n", rolloutFiles)
	}
	lineageRoots := make(map[string]string)
	records := make([]Record, len(all))
	for i, p := range all {
		root, ok := lineageRoots[p.lineage]
		if !ok {
			root = resolveCodexLineage(p.lineage, lineageLinks)
			lineageRoots[p.lineage] = root
		}
		records[i] = p.rec
		records[i].DedupKey = codexDedupKey(root, p.keyPart)
	}
	// Fork replays can sort before their parent file while carrying later,
	// rewritten timestamps. Stable ordering preserves path order for ties.
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Timestamp.Before(records[j].Timestamp)
	})
	return Dedup(records), nil
}

// codexDedupKey builds the cross-file dedup identity for a Codex record. It is
// shared with the test helper so key assertions pin production's formula.
func codexDedupKey(lineageID, keyPart string) string {
	return "codex:" + lineageID + ":" + keyPart
}

func resolveCodexLineage(lineageID string, links map[string]string) string {
	path := make([]string, 0, 4)
	positions := make(map[string]int)
	for lineageID != "" {
		if cycleStart, cycle := positions[lineageID]; cycle {
			root := path[cycleStart]
			for _, candidate := range path[cycleStart+1:] {
				if candidate < root {
					root = candidate
				}
			}
			return root
		}
		positions[lineageID] = len(path)
		path = append(path, lineageID)
		next, ok := links[lineageID]
		// next == lineageID is not checked: a self-edge is refused where links are
		// built, and the cycle branch above already returns the same answer for one.
		if !ok || next == "" {
			return lineageID
		}
		lineageID = next
	}
	return lineageID
}
