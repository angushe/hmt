package main

import (
	"flag"
	"fmt"
	"math"
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

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type sourceDir struct {
	name     string
	dir      string
	optional bool
	scan     func(string) ([]parser.Record, error)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "hmt: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		// Rejected here for the same reason flag.NArg() rejects them below: a
		// stray token must not be silently ignored on one path and fatal on another.
		if len(os.Args) > 2 {
			return fmt.Errorf("unexpected argument %q: hmt version takes no arguments", os.Args[2])
		}
		fmt.Printf("hmt %s (%s, %s)\n", version, commit, buildDate)
		return nil
	}

	by := flag.String("by", "day", "aggregation: day, week, month, session, project")
	since := flag.String("since", "", "start date YYYY-MM-DD (must not be after --until)")
	until := flag.String("until", "", "end date YYYY-MM-DD (must not be before --since)")
	last := flag.String("last", "", "recent period: 7d, 30d, 3m (default 1m if no time filter set)")
	model := flag.String("model", "", "filter by model name")
	project := flag.String("project", "", "filter by project (fuzzy match)")
	format := flag.String("format", "table", "output: table, json, csv, chart")
	tz := flag.String("timezone", "", "timezone for date grouping (e.g., Asia/Shanghai, UTC)")
	height := flag.Int("height", 16, "chart plot height in rows, 6-1000 (used by chart; range enforced for every format)")
	topN := flag.Int("top", 6, "max distinct model stacks in chart, 1-6 (used by chart; range enforced for every format)")
	source := flag.String("source", "all", "data source: claude, codex, all")
	flag.Parse()

	// Go's flag package stops at the first non-flag token, so an unrecognized
	// subcommand would silently reduce every later flag to its default.
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q: hmt takes only flags and the version subcommand", flag.Arg(0))
	}

	switch *format {
	case "table", "json", "csv", "chart":
	default:
		return fmt.Errorf("invalid --format value %q: use table, json, csv, or chart", *format)
	}
	// Validated for every format, not just chart: a value that is a hard error
	// one flag away should not be silently ignored. Checked here rather than at
	// render time so bad bounds fail before scanning the session logs.
	if err := report.ValidateChartOptions(*height, *topN); err != nil {
		return err
	}
	// Checked here with the other flag values, not where the sources are built:
	// otherwise `hmt --source gemini` with HOME unset reports "cannot determine
	// home directory", making a flag-value error depend on the environment.
	switch *source {
	case "claude", "codex", "all":
	default:
		return fmt.Errorf("invalid --source value %q: use claude, codex, or all", *source)
	}

	if *last != "" && (*since != "" || *until != "") {
		return fmt.Errorf("--last and --since/--until are mutually exclusive")
	}
	if *last == "" && *since == "" && *until == "" {
		*last = "1m"
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
	dateSince, dateUntil, err := parseDateBounds(*since, *until)
	if err != nil {
		return err
	}
	if dateSince != nil {
		sinceTime = dateSince
	}
	untilTime = dateUntil
	// A range that can never match is a mistake, not a query. Reporting it as
	// "no data found matching filters" reads as an answer about the data.
	if sinceTime != nil && untilTime != nil && !sinceTime.Before(*untilTime) {
		return fmt.Errorf("--since %s is not before --until %s: the range is empty", *since, *until)
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

	sources := configuredSources(home, *source)

	available, err := selectAvailableSources(sources)
	if err != nil {
		return err
	}

	cachedPricing := filepath.Join(home, ".config", "hmt", "pricing.json")
	table, err := pricing.Load(cachedPricing, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("loading pricing: %w", err)
	}

	records, err := scanAvailableSources(available)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		emitEmpty(*format, *by, "no data found")
		return nil
	}

	filtered := report.Filter(records, sinceTime, untilTime, *model, *project)
	if len(filtered) == 0 {
		emitEmpty(*format, *by, "no data found matching filters")
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
	case "chart":
		if err := report.FormatChart(os.Stdout, rows, *by, *height, *topN); err != nil {
			return err
		}
	}

	return nil
}

// emitEmpty reports an empty result. The machine-readable formats get a valid
// empty document on stdout — otherwise `hmt --format json | jq .` fails to parse
// instead of yielding an empty array — and the human message goes to stderr.
func emitEmpty(format, by, message string) {
	switch format {
	case "json":
		report.FormatJSON(os.Stdout, nil, by)
	case "csv":
		report.FormatCSV(os.Stdout, nil, by)
	default:
		fmt.Println(message)
		return
	}
	fmt.Fprintln(os.Stderr, message)
}

func configuredSources(home, source string) []sourceDir {
	optional := source == "all"
	var sources []sourceDir
	if optional || source == "claude" {
		sources = append(sources, sourceDir{
			name:     "claude",
			dir:      filepath.Join(home, ".claude", "projects"),
			optional: optional,
			scan:     parser.ScanDir,
		})
	}
	if optional || source == "codex" {
		sources = append(sources, sourceDir{
			name:     "codex",
			dir:      filepath.Join(home, ".codex", "sessions"),
			optional: optional,
			scan:     parser.ScanCodexDir,
		})
	}
	// No rejection branch here: run() validates --source before calling, so an
	// unknown value never reaches this function. Duplicating the message would
	// give two copies to drift apart.
	return sources
}

func selectAvailableSources(sources []sourceDir) ([]sourceDir, error) {
	var (
		checked   []string
		available []sourceDir
	)
	for _, s := range sources {
		checked = append(checked, s.dir)
		info, err := os.Stat(s.dir)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("not a directory")
		}
		if os.IsNotExist(err) {
			link := brokenSymlinkPath(s.dir)
			if link == "" {
				continue
			}
			err = fmt.Errorf("broken symlink at %s", link)
		}
		if err != nil {
			if s.optional {
				fmt.Fprintf(os.Stderr, "warning: unable to access %s data directory %s: %v\n", s.name, s.dir, err)
				continue
			}
			return nil, fmt.Errorf("accessing %s data directory %s: %w", s.name, s.dir, err)
		}
		available = append(available, s)
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("no data directories found: %s", strings.Join(checked, ", "))
	}
	return available, nil
}

// brokenSymlinkPath returns the first path at or above dir that is a symlink
// which fails to resolve, or "" when dir is simply absent. A dangling link fails
// os.Stat with ENOENT exactly like a missing directory, but it means a broken
// configuration rather than an unused source — and the link may be an ancestor,
// as with `ln -s /Volumes/ext/codex ~/.codex` when ~/.codex/sessions is
// configured. A symlink that resolves is healthy no matter what is missing
// beneath it, so it must not be reported.
func brokenSymlinkPath(dir string) string {
	for p := dir; ; {
		info, err := os.Lstat(p)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if _, statErr := os.Stat(p); statErr != nil {
					return p
				}
			}
			return ""
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

func scanAvailableSources(sources []sourceDir) ([]parser.Record, error) {
	var (
		records   []parser.Record
		succeeded int
	)
	for _, s := range sources {
		recs, err := s.scan(s.dir)
		if err != nil {
			if s.optional {
				fmt.Fprintf(os.Stderr, "warning: unable to scan %s data at %s: %v\n", s.name, s.dir, err)
				continue
			}
			return nil, fmt.Errorf("scanning %s data: %w", s.name, err)
		}
		succeeded++
		records = append(records, recs...)
	}
	if succeeded == 0 {
		return nil, fmt.Errorf("unable to scan any data source")
	}
	return records, nil
}

func parseDateBounds(since, until string) (*time.Time, *time.Time, error) {
	var sinceTime, untilTime *time.Time
	if since != "" {
		t, err := time.Parse("2006-01-02", since)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid --since date %q: use YYYY-MM-DD", since)
		}
		sinceTime = &t
	}
	if until != "" {
		t, err := time.Parse("2006-01-02", until)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid --until date %q: use YYYY-MM-DD", until)
		}
		t = t.AddDate(0, 0, 1)
		untilTime = &t
	}
	return sinceTime, untilTime, nil
}

func parseDuration(s string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d+)([dm])$`)
	m := re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("expected format like 7d or 3m")
	}
	unit := 24 * time.Hour
	if m[2] == "m" {
		unit = 30 * 24 * time.Hour
	}
	// time.Duration is int64 nanoseconds (~292 years), so an unchecked
	// multiplication wraps into a plausible-looking window instead of failing.
	maxN := int64(math.MaxInt64) / int64(unit)
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > maxN {
		return 0, fmt.Errorf("value too large: maximum is %d%s", maxN, m[2])
	}
	return time.Duration(n) * unit, nil
}
