// tldfinder reports which name.tld combinations are unregistered, using RDAP.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/masonbesmer/tld-finder/internal/check"
	"github.com/masonbesmer/tld-finder/internal/input"
	"github.com/masonbesmer/tld-finder/internal/output"
)

var version = "dev"

const usageText = `usage:
  tldfinder check [flags] <input.csv>   check name × TLD availability via RDAP
  tldfinder tlds  [--tld ...]           which TLDs RDAP can serve
  tldfinder version

Flags must precede the input file. Each flag also reads TLDFINDER_<FLAG_UPPER_SNAKE>.
Run "tldfinder check -h" for flags.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "check":
		return cmdCheck(args[1:])
	case "tlds":
		return cmdTLDs(args[1:])
	case "version":
		fmt.Println("tldfinder " + version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[0], usageText)
		return 2
	}
}

// applyEnv fills unset flags from TLDFINDER_<FLAG_UPPER_SNAKE>.
func applyEnv(fs *flag.FlagSet) error {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if set[f.Name] || err != nil {
			return
		}
		env := "TLDFINDER_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(env); ok {
			if serr := fs.Set(f.Name, v); serr != nil {
				err = fmt.Errorf("%s: %v", env, serr)
			}
		}
	})
	return err
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}

func cacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "tldfinder")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tldfinder")
	}
	return filepath.Join(home, ".cache", "tldfinder")
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func parseDelimiter(s string) (rune, error) {
	if s == `\t` {
		return '\t', nil
	}
	r := []rune(s)
	if len(r) != 1 {
		return 0, fmt.Errorf("invalid --delimiter %q (want a single character)", s)
	}
	return r[0], nil
}

// gatherTLDs normalizes --tld values (comma-split, repeatable) plus the
// newline-delimited --tld-file.
func gatherTLDs(flags multiFlag, file string) ([]string, error) {
	raw := []string{}
	for _, v := range flags {
		raw = append(raw, strings.Split(v, ",")...)
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				raw = append(raw, line)
			}
		}
	}
	var tlds []string
	for _, t := range raw {
		if strings.TrimSpace(t) == "" {
			continue
		}
		nt, err := input.NormalizeTLD(t)
		if err != nil {
			return nil, fmt.Errorf("invalid TLD %q: %v", t, err)
		}
		tlds = append(tlds, nt)
	}
	return tlds, nil
}

func loadBootstrap(ctx context.Context, ttl time.Duration, log *slog.Logger) (check.Bootstrap, error) {
	loader := &check.BootstrapLoader{
		URL:      check.BootstrapURL,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		CacheDir: cacheDir(),
		TTL:      ttl,
		Log:      log,
	}
	return loader.Load(ctx)
}

func rdapHost(boot check.Bootstrap, tld string) string {
	bases := boot[tld]
	if len(bases) == 0 {
		return "unsupported"
	}
	if u, err := url.Parse(bases[0]); err == nil {
		return u.Host
	}
	return bases[0]
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	var tldFlags, rdapBases multiFlag
	fs.Var(&tldFlags, "tld", "comma-separated TLDs (repeatable)")
	fs.Var(&rdapBases, "rdap-base", "tld=https://base-url override for TLDs missing from the IANA bootstrap (repeatable)")
	column := fs.String("column", "", "column holding the name (name or 0-based index; default auto)")
	tldColumn := fs.String("tld-column", "", "per-row TLDs from a column")
	tldFile := fs.String("tld-file", "", "newline-delimited TLD list")
	hasHeader := fs.String("has-header", "auto", "auto, true, or false")
	delimiter := fs.String("delimiter", ",", `field delimiter (\t accepted)`)
	out := fs.String("out", "", "output path (default stdout)")
	format := fs.String("format", "csv", "csv, jsonl, table, or list (newline-separated domains)")
	only := fs.String("only", "", "emit only these statuses, e.g. available")
	timeout := fs.Duration("timeout", 10*time.Second, "per-request timeout")
	retries := fs.Int("retries", 2, "retries per domain")
	rate := fs.Duration("rate", 200*time.Millisecond, "minimum interval per RDAP host")
	bootstrapTTL := fs.Duration("bootstrap-ttl", 24*time.Hour, "RDAP bootstrap cache lifetime")
	dryRun := fs.Bool("dry-run", false, "print expanded domains and RDAP hosts; no checks")
	logLevel := fs.String("log-level", "info", "slog level")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := applyEnv(fs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	log, err := newLogger(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: tldfinder check [flags] <input.csv>")
		return 2
	}

	delim, err := parseDelimiter(*delimiter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var onlyStatuses []check.Status
	if *only != "" {
		valid := map[check.Status]bool{
			check.StatusAvailable: true, check.StatusRegistered: true,
			check.StatusReserved: true, check.StatusUnknown: true, check.StatusError: true,
		}
		for _, s := range strings.Split(*only, ",") {
			st := check.Status(strings.TrimSpace(s))
			if !valid[st] {
				fmt.Fprintf(os.Stderr, "invalid --only status %q\n", s)
				return 2
			}
			onlyStatuses = append(onlyStatuses, st)
		}
	}
	tlds, err := gatherTLDs(tldFlags, *tldFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rows, err := input.ReadCSV(f, input.CSVOptions{
		Delimiter: delim, HasHeader: *hasHeader, Column: *column, TLDColumn: *tldColumn,
	})
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	domains := input.Expand(rows, tlds, log)
	if len(domains) == 0 {
		fmt.Fprintln(os.Stderr, "no domains to check")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	boot, err := loadBootstrap(ctx, *bootstrapTTL, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, spec := range rdapBases {
		if err := boot.Override(spec); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}

	if *dryRun {
		for _, d := range domains {
			fmt.Printf("%s\t%s\n", d.FQDN, rdapHost(boot, d.TLD))
		}
		return 0
	}

	dst := os.Stdout
	if *out != "" {
		if dst, err = os.Create(*out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	buf := bufio.NewWriter(dst)
	w, err := output.New(buf, *format, onlyStatuses)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	runner := &check.Runner{
		Client: &check.Client{
			HTTP:      check.NewHTTPClient(),
			Bootstrap: boot,
			UserAgent: "tldfinder/" + version + " (+https://github.com/masonbesmer/tld-finder)",
			Timeout:   *timeout,
		},
		Rate:    *rate,
		Retries: *retries,
		Log:     log,
	}
	bad := false
	runErr := runner.Run(ctx, domains, func(r check.Result) {
		if r.Status == check.StatusUnknown || r.Status == check.StatusError {
			bad = true
		}
		if werr := w.Write(r); werr != nil {
			log.Error("write failed", "err", werr)
		}
	})

	flushErr := w.Flush()
	if flushErr == nil {
		flushErr = buf.Flush()
	}
	if *out != "" {
		if cerr := dst.Close(); cerr != nil && flushErr == nil {
			flushErr = cerr
		}
	}
	switch {
	case errors.Is(runErr, context.Canceled):
		return 130
	case runErr != nil:
		fmt.Fprintln(os.Stderr, runErr)
		return 1
	case flushErr != nil:
		fmt.Fprintln(os.Stderr, flushErr)
		return 1
	case bad:
		return 3
	}
	return 0
}

func cmdTLDs(args []string) int {
	fs := flag.NewFlagSet("tlds", flag.ContinueOnError)
	var tldFlags, rdapBases multiFlag
	fs.Var(&tldFlags, "tld", "comma-separated TLDs (repeatable)")
	fs.Var(&rdapBases, "rdap-base", "tld=https://base-url override for TLDs missing from the IANA bootstrap (repeatable)")
	bootstrapTTL := fs.Duration("bootstrap-ttl", 24*time.Hour, "RDAP bootstrap cache lifetime")
	logLevel := fs.String("log-level", "info", "slog level")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := applyEnv(fs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	log, err := newLogger(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	tlds, err := gatherTLDs(tldFlags, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	boot, err := loadBootstrap(ctx, *bootstrapTTL, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, spec := range rdapBases {
		if err := boot.Override(spec); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}

	if len(tlds) == 0 {
		for tld := range boot {
			tlds = append(tlds, tld)
		}
		sort.Strings(tlds)
	}
	for _, tld := range tlds {
		base := "unsupported"
		if bases := boot[tld]; len(bases) > 0 {
			base = bases[0]
		}
		fmt.Printf("%s\t%s\n", tld, base)
	}
	return 0
}
