package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/angushe/hmt/internal/parser"
)

func TestRun_ValidatesFormatBeforeSourceDiscovery(t *testing.T) {
	// run() is only guaranteed to return before scanning while the early guards
	// hold; an empty HOME keeps the developer's real logs out of reach if they move.
	t.Setenv("HOME", t.TempDir())
	// The invalid source is a second guard: even if format validation moves, this
	// test still returns before pricing or scanning session data.
	_, err := runCLI(t, "--format", "tabel", "--source", "invalid")
	if err == nil || !strings.Contains(err.Error(), `invalid --format value "tabel"`) {
		t.Fatalf("error = %v, want format validation before source discovery", err)
	}
}

func TestParseDateBounds_UsesUTCAndInclusiveUntil(t *testing.T) {
	since, until, err := parseDateBounds("2026-08-01", "2026-08-03")
	if err != nil {
		t.Fatal(err)
	}
	wantSince := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantUntil := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	if since == nil || !since.Equal(wantSince) {
		t.Errorf("since = %v, want %v", since, wantSince)
	}
	if until == nil || !until.Equal(wantUntil) {
		t.Errorf("until = %v, want exclusive bound %v", until, wantUntil)
	}
}

// TestConfiguredSources_EncodesOptionalPolicy covers the struct shape only:
// which sources each --source value produces, where they point, and whether a
// missing directory is tolerated. That the right scanner is attached to each is
// covered end to end by TestRun_WiresFlagsToPipeline.
func TestConfiguredSources_EncodesOptionalPolicy(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude", "projects")
	codexDir := filepath.Join(home, ".codex", "sessions")

	all := configuredSources(home, "all")
	if len(all) != 2 || all[0].name != "claude" || all[0].dir != claudeDir || !all[0].optional ||
		all[1].name != "codex" || all[1].dir != codexDir || !all[1].optional {
		t.Fatalf("all sources = %+v, want two optional sources", all)
	}

	// An explicit --source is required: a missing directory must error rather
	// than be silently skipped the way it is under "all".
	for _, tc := range []struct {
		source string
		dir    string
	}{
		{"claude", claudeDir},
		{"codex", codexDir},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got := configuredSources(home, tc.source)
			if len(got) != 1 || got[0].name != tc.source || got[0].dir != tc.dir || got[0].optional {
				t.Fatalf("sources = %+v, want one required %s source at %s", got, tc.source, tc.dir)
			}
		})
	}
}

func TestSelectAvailableSources_OptionalSkipsStatErrorButRequiredFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-referential symlink behavior differs on Windows")
	}
	tmp := t.TempDir()
	good := filepath.Join(tmp, "good")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(tmp, "loop")
	if err := os.Symlink(bad, bad); err != nil {
		t.Fatal(err)
	}
	sources := []sourceDir{
		{name: "bad", dir: bad, optional: true},
		{name: "good", dir: good, optional: true},
	}

	var (
		available []sourceDir
		selectErr error
	)
	stderr := captureMainStderr(t, func() {
		available, selectErr = selectAvailableSources(sources)
	})
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if len(available) != 1 || available[0].name != "good" {
		t.Fatalf("available = %+v, want only good source", available)
	}
	if !strings.Contains(stderr, "warning: unable to access bad data directory "+bad) {
		t.Errorf("stderr = %q, want inaccessible-source warning", stderr)
	}

	sources[0].optional = false
	_, err := selectAvailableSources(sources[:1])
	if err == nil || !strings.Contains(err.Error(), "accessing bad data directory "+bad) {
		t.Fatalf("error = %v, want explicit-source access error", err)
	}
}

func TestSelectAvailableSources_RejectsRegularFile(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "sessions")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := selectAvailableSources([]sourceDir{{name: "codex", dir: file}})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want regular-file source rejection", err)
	}
}

func TestScanAvailableSources_OptionalContinuesButRequiredFails(t *testing.T) {
	bad := sourceDir{
		name:     "codex",
		dir:      "/codex",
		optional: true,
		scan: func(string) ([]parser.Record, error) {
			return nil, fmt.Errorf("permission denied")
		},
	}
	good := sourceDir{
		name:     "claude",
		dir:      "/claude",
		optional: true,
		scan: func(string) ([]parser.Record, error) {
			return []parser.Record{{Model: "claude-opus-4-6", InputTokens: 100}}, nil
		},
	}

	var (
		records []parser.Record
		scanErr error
	)
	stderr := captureMainStderr(t, func() {
		records, scanErr = scanAvailableSources([]sourceDir{bad, good})
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 1 || records[0].InputTokens != 100 {
		t.Fatalf("records = %+v, want good source records", records)
	}
	if !strings.Contains(stderr, "warning: unable to scan codex data") {
		t.Errorf("stderr = %q, want optional scan warning", stderr)
	}

	bad.optional = false
	_, err := scanAvailableSources([]sourceDir{bad})
	if err == nil || !strings.Contains(err.Error(), "scanning codex data") {
		t.Fatalf("error = %v, want required scan error", err)
	}
}

func TestScanAvailableSources_ReturnsErrorWhenEveryOptionalSourceFails(t *testing.T) {
	failingSource := func(name string) sourceDir {
		return sourceDir{
			name:     name,
			dir:      "/" + name,
			optional: true,
			scan: func(string) ([]parser.Record, error) {
				return nil, fmt.Errorf("permission denied")
			},
		}
	}
	var scanErr error
	captureMainStderr(t, func() {
		_, scanErr = scanAvailableSources([]sourceDir{failingSource("claude"), failingSource("codex")})
	})
	if scanErr == nil || !strings.Contains(scanErr.Error(), "unable to scan any data source") {
		t.Fatalf("error = %v, want all-sources-failed error", scanErr)
	}
}

func TestSelectAvailableSources_SkipsAbsentDirectoriesSilently(t *testing.T) {
	tmp := t.TempDir()
	present := filepath.Join(tmp, "claude")
	if err := os.Mkdir(present, 0o755); err != nil {
		t.Fatal(err)
	}
	sources := []sourceDir{
		{name: "claude", dir: present, optional: true},
		{name: "codex", dir: filepath.Join(tmp, "codex"), optional: true},
	}

	var (
		available []sourceDir
		selectErr error
	)
	stderr := captureMainStderr(t, func() {
		available, selectErr = selectAvailableSources(sources)
	})
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if len(available) != 1 || available[0].name != "claude" {
		t.Fatalf("available = %+v, want only the existing source", available)
	}
	// Claude-only machines have no ~/.codex/sessions; warning there would fire
	// on every run and break the backward-compatible default.
	if stderr != "" {
		t.Errorf("stderr = %q, want silence for a merely absent directory", stderr)
	}
}

func TestSelectAvailableSources_ErrorsWhenNoDirectoryExists(t *testing.T) {
	tmp := t.TempDir()
	claude := filepath.Join(tmp, "claude")
	codex := filepath.Join(tmp, "codex")

	_, err := selectAvailableSources([]sourceDir{
		{name: "claude", dir: claude, optional: true},
		{name: "codex", dir: codex, optional: true},
	})
	if err == nil || !strings.Contains(err.Error(), "no data directories found: "+claude+", "+codex) {
		t.Fatalf("error = %v, want error listing both checked directories", err)
	}
}

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "1m", want: 30 * 24 * time.Hour},
		{in: " 3m ", want: 90 * 24 * time.Hour},
		{in: "0d", want: 0},
		{in: "7", wantErr: "expected format like 7d or 3m"},
		{in: "7y", wantErr: "expected format like 7d or 3m"},
		{in: "-1d", wantErr: "expected format like 7d or 3m"},
		// time.Duration saturates just above these; beyond them an unchecked
		// multiply would wrap into a plausible-looking short window.
		{in: "106751d", want: 106751 * 24 * time.Hour},
		{in: "106752d", wantErr: "value too large: maximum is 106751d"},
		{in: "213505d", wantErr: "value too large: maximum is 106751d"},
		{in: "3558m", want: 3558 * 30 * 24 * time.Hour},
		{in: "7117m", wantErr: "value too large: maximum is 3558m"},
		{in: "99999999999999999999999d", wantErr: "value too large: maximum is 106751d"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDuration(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRun_RejectsPositionalArgument(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runCLI(t, "today", "--format", "csv", "--by", "month")
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "today"`) {
		t.Fatalf("error = %v, want positional-argument rejection", err)
	}
}

// TestRun_RejectsInvalidFlagValues pins the user-facing error strings on run()'s
// uncovered error paths. Each was reachable only through run(), so the helper
// unit tests could not see them.
func TestRun_RejectsInvalidFlagValues(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"invalid --last":     {[]string{"--last", "7w"}, `invalid --last value "7w"`},
		"invalid --since":    {[]string{"--since", "08-2026"}, `invalid --since date "08-2026"`},
		"invalid --until":    {[]string{"--until", "not-a-date"}, `invalid --until date "not-a-date"`},
		"invalid --source":   {[]string{"--source", "gemini"}, `invalid --source value "gemini"`},
		"empty date range":   {[]string{"--since", "2027-01-01", "--until", "2020-01-01"}, `--since 2027-01-01 is not before --until 2020-01-01: the range is empty`},
		"--height off chart": {[]string{"--format", "table", "--height", "5"}, "--height must be at least 6"},
		"--top off chart":    {[]string{"--format", "table", "--top", "9"}, "--top must be at most 6"},
	} {
		t.Run(name, func(t *testing.T) {
			// An empty HOME has no sources, so anything that reached discovery
			// would report "no data directories found" instead.
			t.Setenv("HOME", t.TempDir())
			_, err := runCLI(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestRun_ValidatesFlagValuesWithoutTouchingTheEnvironment: a flag-value error
// must not depend on HOME being resolvable, or `hmt --source gemini` on a
// machine with HOME unset reports "cannot determine home directory".
func TestRun_ValidatesFlagValuesWithoutTouchingTheEnvironment(t *testing.T) {
	t.Setenv("HOME", "")
	for name, tc := range map[string]struct{ args []string }{
		"--source": {[]string{"--source", "gemini"}},
		"--format": {[]string{"--format", "tabel"}},
		"--top":    {[]string{"--top", "9"}},
		"range":    {[]string{"--since", "2027-01-01", "--until", "2020-01-01"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runCLI(t, tc.args...)
			if err == nil || strings.Contains(err.Error(), "home directory") {
				t.Fatalf("error = %v, want the flag-value error rather than an environment one", err)
			}
		})
	}
}

// TestRun_ReportsAnEmptyScanBeforeFiltering covers the unfiltered empty branch;
// only the post-filter variant was exercised.
func TestRun_ReportsAnEmptyScanBeforeFiltering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedPricingCache(t, home)
	// The directory exists but holds no sessions, so the scan yields nothing and
	// the filters are never consulted.
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "--format", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no data found") || strings.Contains(out, "matching filters") {
		t.Errorf("stdout = %q, want the pre-filter empty message", out)
	}
}

func TestRun_VersionSubcommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	// The flag.NArg() guard would turn a broken early return into
	// `unexpected argument "version"` rather than a harmless default report.
	if !strings.Contains(out, "hmt "+version) {
		t.Errorf("stdout = %q, want version output", out)
	}
}

// TestRun_VersionRejectsTrailingArguments: the flag.NArg() guard exists so a
// stray token cannot silently change behaviour; the version path opted out.
func TestRun_VersionRejectsTrailingArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runCLI(t, "version", "extra")
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "extra"`) {
		t.Fatalf("error = %v, want trailing-argument rejection", err)
	}
}

func TestSelectAvailableSources_ReportsBrokenSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	tmp := t.TempDir()
	good := filepath.Join(tmp, "claude")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "sessions")
	if err := os.Symlink(filepath.Join(tmp, "gone"), link); err != nil {
		t.Fatal(err)
	}

	var (
		available []sourceDir
		selectErr error
	)
	stderr := captureMainStderr(t, func() {
		available, selectErr = selectAvailableSources([]sourceDir{
			{name: "claude", dir: good, optional: true},
			{name: "codex", dir: link, optional: true},
		})
	})
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if len(available) != 1 || available[0].name != "claude" {
		t.Fatalf("available = %+v, want the healthy source only", available)
	}
	// A dangling symlink fails os.Stat with ENOENT just like an absent path, but
	// a configured-and-broken source must not be read as an unused one.
	if !strings.Contains(stderr, "warning: unable to access codex data directory "+link+": broken symlink") {
		t.Errorf("stderr = %q, want broken-symlink warning", stderr)
	}

	_, err := selectAvailableSources([]sourceDir{{name: "codex", dir: link}})
	if err == nil || !strings.Contains(err.Error(), "broken symlink") {
		t.Fatalf("error = %v, want required-source broken-symlink error", err)
	}
}

// TestSelectAvailableSources_ReportsBrokenSymlinkAncestor covers the shape the
// leaf check misses: `ln -s /Volumes/ext/codex ~/.codex` with ~/.codex/sessions
// configured, where both Stat and Lstat on the configured path fail with ENOENT.
func TestSelectAvailableSources_ReportsBrokenSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	tmp := t.TempDir()
	ancestor := filepath.Join(tmp, "codexroot")
	if err := os.Symlink(filepath.Join(tmp, "unmounted"), ancestor); err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(ancestor, "sessions")

	var selectErr error
	stderr := captureMainStderr(t, func() {
		_, selectErr = selectAvailableSources([]sourceDir{{name: "codex", dir: configured, optional: true}})
	})
	if selectErr == nil || !strings.Contains(selectErr.Error(), "no data directories found") {
		t.Fatalf("error = %v, want no-directories error once the only source is broken", selectErr)
	}
	if !strings.Contains(stderr, "broken symlink at "+ancestor) {
		t.Errorf("stderr = %q, want the warning to name the dangling ancestor", stderr)
	}
}

func TestBrokenSymlinkPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(tmp, "dangling")
	if err := os.Symlink(filepath.Join(tmp, "missing"), dangling); err != nil {
		t.Fatal(err)
	}

	// A symlink that resolves is healthy no matter what is missing beneath it —
	// reporting it sends the user to fix a link that is fine, and warns on every
	// run for anyone who symlinks ~/.codex or whose $HOME is itself a symlink.
	healthy := filepath.Join(tmp, "healthy")
	if err := os.Symlink(realDir, healthy); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ dir, want string }{
		"existing directory":       {realDir, ""},
		"absent path":              {filepath.Join(tmp, "nope"), ""},
		"absent under a real":      {filepath.Join(realDir, "nope"), ""},
		"working symlink":          {healthy, ""},
		"absent under a working":   {filepath.Join(healthy, "sessions"), ""},
		"absent deep under a link": {filepath.Join(healthy, "a", "b"), ""},
		"dangling leaf":            {dangling, dangling},
		"dangling ancestor":        {filepath.Join(dangling, "sessions"), dangling},
	} {
		t.Run(name, func(t *testing.T) {
			if got := brokenSymlinkPath(tc.dir); got != tc.want {
				t.Errorf("brokenSymlinkPath(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

// TestRun_WiresFlagsToPipeline pins the handoffs inside run() that the helper
// tests cannot reach: --source, --model, --timezone and ComputeCosts can each
// be severed on their own and still compile.
func TestRun_WiresFlagsToPipeline(t *testing.T) {
	const claudeModel = "claude-opus-4-6"
	const codexModel = "gpt-5.5"
	// writeClaudeFixture records /Users/alice/work/project; a different cwd here
	// is what makes the --project handoff observable.
	const claudeProject = "work/project"
	const codexProject = "other/codexproj"

	home := t.TempDir()
	t.Setenv("HOME", home)
	seedPricingCache(t, home)
	// 23:30Z lands on the next day in Asia/Shanghai, which is what makes the
	// --timezone handoff observable at all.
	writeClaudeFixture(t, home, []time.Time{time.Date(2026, 3, 15, 23, 30, 0, 0, time.UTC)})
	writeCodexFixture(t, home, "2026-03-15T10:00:00Z", codexModel, "/Users/alice/"+codexProject)

	window := []string{"--format", "csv", "--by", "day", "--since", "2026-03-01", "--until", "2026-03-31"}
	runWith := func(t *testing.T, extra ...string) string {
		t.Helper()
		out, err := runCLI(t, append(slices.Clone(window), extra...)...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("--source selects the scanners", func(t *testing.T) {
		for _, tc := range []struct {
			source string
			want   []string
		}{
			{"claude", []string{claudeModel}},
			{"codex", []string{codexModel}},
			{"all", []string{claudeModel, codexModel}},
		} {
			t.Run(tc.source, func(t *testing.T) {
				got := csvColumn(t, runWith(t, "--source", tc.source, "--timezone", "UTC"), csvModelCol)
				if !slices.Equal(got, tc.want) {
					t.Errorf("models = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("costs are computed", func(t *testing.T) {
		rows := csvRows(t, runWith(t, "--source", "all", "--timezone", "UTC"))
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want one per source", len(rows))
		}
		for _, row := range rows {
			if row[csvHasCostCol] != "true" || row[csvCostCol] == "" {
				t.Errorf("row %v: want a computed cost, got cost=%q has_cost=%q",
					row, row[csvCostCol], row[csvHasCostCol])
			}
		}
	})

	t.Run("--model filters", func(t *testing.T) {
		got := csvColumn(t, runWith(t, "--source", "all", "--timezone", "UTC", "--model", codexModel), csvModelCol)
		if !slices.Equal(got, []string{codexModel}) {
			t.Errorf("models = %v, want only %q", got, codexModel)
		}
	})

	t.Run("--project filters", func(t *testing.T) {
		got := csvColumn(t, runWith(t, "--source", "all", "--timezone", "UTC", "--project", "codexproj"), csvModelCol)
		if !slices.Equal(got, []string{codexModel}) {
			t.Errorf("models = %v, want only the Codex project's rows", got)
		}
	})

	// Not just which grouping the formatter is told about, but which formatter
	// runs at all: each arm of run()'s format switch is its own handoff.
	t.Run("--format selects the formatter", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		for _, tc := range []struct{ format, want string }{
			{"json", `"has_cost"`},
			{"csv", "day,model,input_tokens"},
			{"table", "│"},
			{"chart", "█"},
		} {
			t.Run(tc.format, func(t *testing.T) {
				out := runWith(t, "--source", "all", "--timezone", "UTC", "--format", tc.format)
				if !strings.Contains(out, tc.want) {
					t.Errorf("--format %s output = %q, want it to contain %q", tc.format, out, tc.want)
				}
			})
		}
	})

	t.Run("--by reaches the aggregator and the formatter", func(t *testing.T) {
		out := runWith(t, "--source", "all", "--timezone", "UTC", "--by", "project")
		header, _, _ := strings.Cut(out, "\n")
		if first, _, _ := strings.Cut(header, ","); first != "project" {
			t.Errorf("header = %q, want the grouping name in the first column", header)
		}
		got := csvKeys(t, out)
		want := []string{codexProject, claudeProject}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("keys = %v, want project names %v", got, want)
		}

		// Each formatter receives the label separately, so CSV agreeing does not
		// imply the table does.
		table := runWith(t, "--source", "all", "--timezone", "UTC", "--by", "project", "--format", "table")
		tableHeader, _, _ := strings.Cut(table, "\n")
		if _, rest, _ := strings.Cut(table, "\n"); !strings.Contains(rest, "PROJECT") {
			t.Errorf("table header = %q, want the grouping name; first line %q", rest, tableHeader)
		}

		// The chart's copy is load-bearing beyond the title: keyName also drives
		// x-label formatting, bucket ordering (name-ascending for time keys vs
		// cost-descending otherwise) and which end truncation drops.
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		chart := runWith(t, "--source", "all", "--timezone", "UTC", "--by", "project", "--format", "chart")
		chartTitle, _, _ := strings.Cut(chart, "\n")
		if !strings.Contains(chartTitle, "Cost by project") {
			t.Errorf("chart title = %q, want the grouping name", chartTitle)
		}
	})

	// Every --by arm, not just the one an earlier round happened to name: each
	// case in run()'s switch is its own handoff, and swapping two of them
	// (week→ByDay, session→ByProject) leaves the data correct under a header
	// that lies about the grouping.
	t.Run("every --by value reaches its own aggregator", func(t *testing.T) {
		for _, tc := range []struct{ by, wantKey string }{
			{"day", "2026-03-15"},
			{"week", "2026-W11"},
			{"month", "2026-03"},
			{"project", claudeProject},
			{"session", "sess-0"},
		} {
			t.Run(tc.by, func(t *testing.T) {
				got := csvKeys(t, runWith(t, "--source", "claude", "--timezone", "UTC", "--by", tc.by))
				if !slices.Equal(got, []string{tc.wantKey}) {
					t.Errorf("--by %s keys = %v, want [%s]", tc.by, got, tc.wantKey)
				}
			})
		}
	})

	t.Run("invalid --by is rejected rather than defaulted", func(t *testing.T) {
		_, err := runCLI(t, "--by", "quarter")
		if err == nil || !strings.Contains(err.Error(), `invalid --by value "quarter"`) {
			t.Fatalf("error = %v, want invalid-grouping error", err)
		}
	})

	t.Run("invalid --timezone is rejected rather than ignored", func(t *testing.T) {
		_, err := runCLI(t, "--timezone", "Mars/Olympus")
		if err == nil || !strings.Contains(err.Error(), `invalid --timezone "Mars/Olympus"`) {
			t.Fatalf("error = %v, want invalid-timezone error", err)
		}
	})

	// The default must stay "all": flipping it to "claude" would silently drop
	// every Codex user's data with no flag, no warning and no failing test.
	t.Run("the default --source is every source", func(t *testing.T) {
		got := csvColumn(t, runWith(t, "--timezone", "UTC"), csvModelCol)
		if !slices.Equal(got, []string{claudeModel, codexModel}) {
			t.Errorf("models = %v, want both sources scanned by default", got)
		}
	})

	t.Run("--last and --since stay mutually exclusive", func(t *testing.T) {
		_, err := runCLI(t, "--last", "7d", "--since", "2026-01-01")
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("error = %v, want mutual-exclusion error", err)
		}
	})

	t.Run("--height and --top reach the chart", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")
		chartLines := func(height string) int {
			return strings.Count(runWith(t, "--source", "all", "--timezone", "UTC",
				"--format", "chart", "--height", height), "\n")
		}
		// Only the plot rows scale with --height; the framing is fixed.
		if short, tall := chartLines("8"), chartLines("14"); tall-short != 6 {
			t.Errorf("chart lines at height 8 and 14 = %d and %d, want a difference of 6", short, tall)
		}
		out := runWith(t, "--source", "all", "--timezone", "UTC", "--format", "chart", "--top", "1")
		if !strings.Contains(out, "other") {
			t.Errorf("--top 1 should collapse all but one model into 'other':\n%s", out)
		}
	})

	t.Run("--timezone shifts day boundaries", func(t *testing.T) {
		utc := csvKeys(t, runWith(t, "--source", "claude", "--timezone", "UTC"))
		shanghai := csvKeys(t, runWith(t, "--source", "claude", "--timezone", "Asia/Shanghai"))
		if !slices.Equal(utc, []string{"2026-03-15"}) {
			t.Errorf("UTC days = %v, want [2026-03-15] for a 23:30Z record", utc)
		}
		if !slices.Equal(shanghai, []string{"2026-03-16"}) {
			t.Errorf("Asia/Shanghai days = %v, want [2026-03-16] for a 23:30Z record", shanghai)
		}
	})
}

// TestRun_WiresTimeFiltersEndToEnd covers the orchestration in run() that unit
// tests cannot reach: that parseDuration's default window and parseDateBounds'
// two bounds are actually handed to report.Filter.
func TestRun_WiresTimeFiltersEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedPricingCache(t, home)

	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	past := now.AddDate(0, 0, -40)
	middle := now.AddDate(0, 0, -5)
	recent := now.AddDate(0, 0, -2)
	writeClaudeFixture(t, home, []time.Time{past, middle, recent})
	day := func(ts time.Time) string { return ts.Format("2006-01-02") }

	t.Run("default window is one month", func(t *testing.T) {
		out, err := runCLI(t, "--format", "csv", "--by", "day", "--timezone", "UTC")
		if err != nil {
			t.Fatal(err)
		}
		got := csvKeys(t, out)
		want := []string{day(middle), day(recent)}
		if !slices.Equal(got, want) {
			t.Errorf("days = %v, want %v (default --last 1m excludes %s)", got, want, day(past))
		}
	})

	t.Run("explicit bounds are both applied", func(t *testing.T) {
		out, err := runCLI(t, "--format", "csv", "--by", "day", "--timezone", "UTC",
			"--since", day(middle), "--until", day(middle))
		if err != nil {
			t.Fatal(err)
		}
		got := csvKeys(t, out)
		want := []string{day(middle)}
		if !slices.Equal(got, want) {
			t.Errorf("days = %v, want %v (--since must exclude %s, --until must exclude %s)",
				got, want, day(past), day(recent))
		}
	})
}

// TestRun_ChecksSourcesBeforeLoadingPricing pins the ordering commit c106617
// established: on a machine with neither source, hmt must fail fast rather than
// make a network request and write a cache file first.
func TestRun_ChecksSourcesBeforeLoadingPricing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := runCLI(t, "--format", "csv")
	if err == nil || !strings.Contains(err.Error(), "no data directories found") {
		t.Fatalf("error = %v, want the source-discovery error", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "hmt")); !os.IsNotExist(statErr) {
		t.Errorf("pricing cache directory exists (stat err = %v); pricing must not load before sources are checked", statErr)
	}
}

// TestRun_ValidatesChartBoundsBeforeScanning keeps bad --height/--top from
// costing a full scan before erroring.
func TestRun_ValidatesChartBoundsBeforeScanning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, tc := range []struct{ flag, value, want string }{
		{"--height", "5", "--height must be at least 6"},
		{"--top", "0", "--top must be at least 1"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			// An empty HOME has no sources, so reaching discovery would produce
			// "no data directories found" instead of the bounds error.
			_, err := runCLI(t, "--format", "chart", tc.flag, tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q before source discovery", err, tc.want)
			}
		})
	}
}

// TestRun_EmptyResultStaysMachineReadable keeps `hmt --format json | jq .` from
// failing to parse when nothing matches.
func TestRun_EmptyResultStaysMachineReadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedPricingCache(t, home)
	writeClaudeFixture(t, home, []time.Time{time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)})

	// A window the fixture cannot fall into.
	empty := []string{"--since", "2020-01-01", "--until", "2020-01-02"}

	for _, tc := range []struct{ format, wantStdout string }{
		{"json", "[]"},
		{"csv", "day,model,input_tokens,output_tokens,cache_write_tokens,cache_read_tokens,cost,has_cost"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var (
				out    string
				runErr error
			)
			stderr := captureMainStderr(t, func() {
				out, runErr = runCLI(t, append([]string{"--format", tc.format}, empty...)...)
			})
			if runErr != nil {
				t.Fatal(runErr)
			}
			if strings.TrimSpace(out) != tc.wantStdout {
				t.Errorf("stdout = %q, want a valid empty %s document %q", out, tc.format, tc.wantStdout)
			}
			if !strings.Contains(stderr, "no data found matching filters") {
				t.Errorf("stderr = %q, want the human message off stdout", stderr)
			}
		})
	}

	t.Run("table keeps the message on stdout", func(t *testing.T) {
		out, err := runCLI(t, append([]string{"--format", "table"}, empty...)...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "no data found matching filters") {
			t.Errorf("stdout = %q, want the human message for a human format", out)
		}
	})
}

// writeClaudeFixture writes one assistant line per timestamp into a Claude
// project log under home.
func writeClaudeFixture(t *testing.T, home string, stamps []time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-Users-alice-work-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, len(stamps))
	for i, ts := range stamps {
		lines = append(lines, fmt.Sprintf(
			`{"type":"assistant","cwd":"/Users/alice/work/project","sessionId":"sess-%d","requestId":"req-%d","timestamp":%q,"message":{"id":"msg-%d","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":10}}}`,
			i, i, ts.Format(time.RFC3339Nano), i))
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCodexFixture writes a single-turn Codex rollout under home, recording cwd
// as the session's working directory.
func writeCodexFixture(t *testing.T, home, timestamp, model, cwd string) {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "03", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	usage := `{"input_tokens":200,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":220}`
	lines := []string{
		fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"session_id":"codex-session","id":"codex-session","cwd":%q}}`, timestamp, cwd),
		fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":%q}}`, timestamp, model),
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s}}}`, timestamp, usage, usage),
	}
	path := filepath.Join(dir, "rollout-2026-03-15T10-00-00-codex-session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedPricingCache writes a fresh cache carrying the current schema marker, so
// pricing.Load is satisfied without reaching the network.
func seedPricingCache(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "hmt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := `{"__hmt_cache_schema_v1__":{},` +
		`"claude-opus-4-6":{"input_cost_per_token":0.000015,"output_cost_per_token":0.000075},` +
		`"gpt-5.5":{"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001}}`
	if err := os.WriteFile(filepath.Join(dir, "pricing.json"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCLI invokes run() with the given arguments and returns its stdout.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	oldArgs, oldFlags, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldFlags, oldStdout
	}()
	flag.CommandLine = flag.NewFlagSet("hmt-test", flag.ContinueOnError)
	os.Args = append([]string{"hmt"}, args...)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	runErr := run()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out, runErr
}

// Column positions in FormatCSV output.
const (
	csvKeyCol = iota
	csvModelCol
	_ // input_tokens
	_ // output_tokens
	_ // cache_write_tokens
	_ // cache_read_tokens
	csvCostCol
	csvHasCostCol
)

// csvRows parses CSV output and returns the data rows, without the header.
func csvRows(t *testing.T, out string) [][]string {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("parsing CSV %q: %v", out, err)
	}
	if len(records) == 0 {
		t.Fatalf("CSV output %q has no header", out)
	}
	return records[1:]
}

// csvColumn returns the sorted distinct values of one column. Sorted so the
// assertion is about which values survive, not about row order.
func csvColumn(t *testing.T, out string, col int) []string {
	t.Helper()
	seen := make(map[string]bool)
	for _, rec := range csvRows(t, out) {
		seen[rec[col]] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

func csvKeys(t *testing.T, out string) []string {
	t.Helper()
	return csvColumn(t, out, csvKeyCol)
}

func captureMainStderr(t *testing.T, fn func()) string {
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
