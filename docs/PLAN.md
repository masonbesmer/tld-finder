# tldfinder — implementation plan

CLI that reads a CSV of candidate names and reports which `name.tld` combinations
are unregistered, using RDAP.

Target reader: an implementing agent. Every section is normative. Anything not
described here is out of scope — see §10.

---

## 0. Scope, and what it buys us

**~100 lookups per day, one run, one source (RDAP).**

Consequences, applied throughout:

- **Serial execution.** 100 RDAP requests paced at 5/s finish in ~20s. No worker
  pool, no `errgroup`, no result-ordering flag, no locking.
- **No captcha handling.** RDAP is an HTTP API published by the registries. There
  is no bot-detection anywhere in this program. (Captcha walls only exist on
  scraped web sources, which §10 excludes.)
- **No WHOIS, no scraping, no pricing.**
- **No source-selection layer.** One source means one concrete client, not a
  `Checker` interface with a single implementation. §4.4 marks where the seam goes
  if a second source is ever added.
- **Pacing is a `time.Sleep`,** not a token-bucket package.
- **No resume journal.** A run is shorter than the time it takes to decide to
  resume one. Ctrl-C and re-run.

Total dependency count: **one** (`golang.org/x/net/idna`). Everything else stdlib.

**The coverage limit this creates:** RDAP is mandatory for every gTLD (`.com`,
`.io`, `.dev`, `.app`, and all new gTLDs) and is served by ~200 ccTLDs, but some
ccTLDs — notably `.de` — have no RDAP endpoint. Those TLDs return
`StatusUnknown`, and `tldfinder tlds` (§7) reports that up front so it is never a
surprise mid-run. If a TLD you actually use lands there, WHOIS port 43 is the fix,
and it is a contained addition at §4.4's seam.

---

## 1. Decisions

### 1.1 Language: Go 1.26

`go 1.26` in `go.mod`, toolchain pinned. Single static binary; stdlib covers HTTP
and CSV outright.

### 1.2 Source: RDAP, not TLD-List

The original brief named TLD-List. It is a **registrar-price comparison** site
behind Cloudflare Turnstile; using it as the availability oracle means scraping
and bot-detection for data the registries publish for free, and it is what forced
a captcha subsystem into earlier drafts of this plan. RDAP (RFC 7480, bootstrap
RFC 9224) is the authoritative, free, unauthenticated, captcha-free answer.

Rate limits are respected, not routed around.

### 1.3 Dependencies

| Concern | Choice | Why not X |
|---|---|---|
| CLI/flags | stdlib `flag` + `flag.NewFlagSet` per subcommand | 3 subcommands doesn't justify cobra. |
| HTTP | stdlib `net/http` | — |
| IDN | `golang.org/x/net/idna` | Punycode + UTS-46 is not hand-rollable. |
| Pacing | `map[host]time.Time` + `time.Sleep` (~10 LOC) | `x/time/rate` is for concurrent contention. There is none. |
| Retry | hand-rolled (~25 LOC) | — |
| Logging | `log/slog` to stderr | — |
| Config | flags + env | No config format to maintain. |
| CSV | `encoding/csv` | — |
| Tests | `testing` + `httptest` + golden files | No testify. |

Module path: `github.com/<owner>/tldfinder`.

---

## 2. Repo structure

```
.
├── cmd/tldfinder/main.go        # flags, subcommand dispatch, wiring, exit codes
├── internal/
│   ├── input/
│   │   ├── csv.go               # CSV read, header sniff, column mapping
│   │   └── name.go              # IDNA + label validation, name × TLD expansion
│   ├── check/
│   │   ├── check.go             # Status, Result, sentinel errors
│   │   ├── bootstrap.go         # IANA RDAP bootstrap fetch + disk cache
│   │   ├── rdap.go              # RDAP client
│   │   └── run.go               # serial loop: pacing, retry, emit
│   └── output/writer.go         # csv | jsonl | table
├── testdata/
│   ├── input/*.csv
│   ├── rdap/*.json
│   └── golden/*.csv
├── docs/PLAN.md
├── .github/workflows/ci.yml
├── .golangci.yml
├── Makefile
├── go.mod
└── README.md
```

No `/pkg` — nothing here is a library. Everything `internal/`, compiler-enforced.

---

## 3. Core types

```go
// internal/check/check.go
package check

type Status string

const (
    StatusAvailable  Status = "available"  // registry has no object for this name
    StatusRegistered Status = "registered"
    StatusReserved   Status = "reserved"   // registry-blocked, not buyable
    StatusUnknown    Status = "unknown"    // no RDAP endpoint for this TLD
    StatusError      Status = "error"      // transport/parse failure after retries
)

type Result struct {
    Input     string        `json:"input"`      // original CSV cell
    Domain    string        `json:"domain"`     // normalized A-label FQDN, no trailing dot
    TLD       string        `json:"tld"`
    Status    Status        `json:"status"`
    Authority string        `json:"authority"`  // RDAP base URL that answered
    CheckedAt time.Time     `json:"checked_at"`
    Latency   time.Duration `json:"latency_ms"`
    Expiry    *time.Time    `json:"expiry,omitempty"` // when registered
    Note      string        `json:"note,omitempty"`
    Err       string        `json:"error,omitempty"`
}
```

`StatusAvailable` means *the registry has no object for this name*. It does **not**
mean purchasable — reserved lists, ICANN collision lists, and premium tiers are
distinct, and RDAP does not price anything. Never print "you can buy this"; print
the status.

Sentinels: `ErrNoRDAP` (no endpoint for this TLD), `ErrRateLimited` (carries
`RetryAfter time.Duration`).

---

## 4. RDAP

### 4.1 Bootstrap — `bootstrap.go`

`GET https://data.iana.org/rdap/dns.json` (RFC 9224), cached to
`$XDG_CACHE_HOME/tldfinder/rdap-bootstrap.json` (falling back to
`~/.cache/tldfinder/`), TTL `--bootstrap-ttl` (24h), honoring `ETag` /
`If-None-Match`.

Parse into `map[tld][]baseURL`. The file's `services` array is
`[[tlds...],[urls...]]` pairs — flatten it. Normalize: lowercase TLDs, strip any
trailing `/` from base URLs, keep only `https`.

On fetch failure: use a stale cache and warn. With no cache at all and no network,
this is a fatal startup error (exit 1) — the program cannot do anything without it.

### 4.2 Query — `rdap.go`

`GET {base}/domain/{ascii-fqdn}` with:
- `Accept: application/rdap+json`
- `User-Agent: tldfinder/<version> (+<repo-url>)`
- per-request timeout `--timeout` (default 10s) via `http.NewRequestWithContext`

| Code | Mapping |
|---|---|
| 200 | `StatusRegistered`. Parse `events[]` for `eventAction == "expiration"` → `Expiry`. If `status[]` contains `reserved` or `pending delete`, record it in `Note`. |
| 404 | `StatusAvailable` (RFC 7480 §5.3 — 404 means the object does not exist). |
| 429 | `ErrRateLimited`; parse `Retry-After` in **both** forms (delta-seconds and HTTP-date). |
| 400 | Some registries use 400 for blocked or invalid labels → `StatusReserved` if the body's `title` matches `/reserv\|blocked\|invalid/i`, else `StatusError`. |
| 3xx | Follow up to 5 redirects — RDAP redirects are legitimate and expected. |
| 5xx, transport error | Retryable (§5.2). |

Try each base URL for the TLD in order; record the one that answered in
`Result.Authority`. A TLD absent from the bootstrap map yields `ErrNoRDAP` →
`StatusUnknown` with `Note: "no RDAP endpoint for .<tld>"`.

Decode only the fields needed — `objectClassName`, `ldhName`, `status`, `events`,
`title` — into a narrow struct. Do not model the full RDAP schema.

### 4.3 Response body caution

Cap the response body with `io.LimitReader` at 1 MiB before decoding. Some
registries return large nested entity graphs, and none of it is needed here.

### 4.4 The extension seam (not built)

If a second source is ever needed — WHOIS port 43 for `.de` and friends — extract
this interface at that point, not before:

```go
type Checker interface {
    Supports(tld string) bool
    Check(ctx context.Context, domain string) (Result, error)
}
```
`run.go` would then iterate sources in rank order, treating a
`ErrNotAuthoritative` sentinel as "fall through to the next". Building that
indirection now, with exactly one implementation, is speculative.

---

## 5. Run loop — `run.go`

```
read CSV
  → expand names × TLDs, normalize, dedupe, drop invalid (warn)
  → for each domain, in input order:
        if no RDAP endpoint for tld → StatusUnknown, emit, continue
        pace(authorityHost)                    // §5.1
        result, err := withRetry(rdap.Check)   // §5.2
        on ErrRateLimited → back off RetryAfter, retry
        on exhaustion     → StatusError, record err
        emit
  → writer
```

Input order is preserved for free because execution is serial.

**5.1 Pacing.** A `map[host]time.Time` of last contact; before each request, sleep
the remainder of that host's minimum interval (`--rate`, default 200ms). A 429
doubles the interval for that host for the rest of the run.

Key on the **RDAP base host actually contacted**, never the TLD — many TLDs share
one RDAP server (all of Verisign's, all of Identity Digital's), and keying by TLD
would let a multi-TLD run hammer a single host.

**5.2 Retry.** Max `--retries` (default 2) on 5xx, transport errors, and 429.
Backoff `500ms * 2^n` with full jitter, capped at 30s and at the remaining ctx
deadline. Honor a 429's `Retry-After` over the computed backoff when it is larger.

**5.3 Signals.** `signal.NotifyContext` on SIGINT/SIGTERM: finish the in-flight
request, flush the writer, exit 130.

---

## 6. Input handling

### 6.1 Input CSV

One column of names, minimum:

```csv
name,notes
acme,client A
"north star",client B
café,idn test
```

### 6.2 Expansion — `internal/input/name.go`

1. Trim, lowercase.
2. If the cell contains a dot **and** its suffix is in the TLD set, treat it as a
   full domain — do not re-append.
3. Internal spaces → `-`; drop characters outside `[a-z0-9-]` after IDNA. Log each
   rewrite at debug; keep the original cell verbatim in `Result.Input`.
4. IDNA: `idna.Lookup.ToASCII` (UTS-46, strict). Failure ⇒ skip the row with a
   warning, do not abort the run.
5. Validate: each label 1–63 octets, no leading or trailing `-`, no `--` in
   positions 3–4 unless the label starts with `xn--`, total FQDN ≤ 253.
6. Cartesian product with the TLD set; dedupe on the final ASCII FQDN.

### 6.3 Output

CSV header, always written:
```
input,domain,tld,status,authority,checked_at,latency_ms,expiry,note,error
```
Timestamps RFC 3339 UTC; empty string for absent optionals. `jsonl` = one `Result`
per line using the §3 tags. `table` = aligned `text/tabwriter`, for humans, never
for piping. `list` = `Result.Domain`, one per line, no header — for piping into
another tool.

---

## 7. CLI

```
tldfinder check [flags] <input.csv>
tldfinder tlds  [--tld ...]        # which of these TLDs RDAP can serve
tldfinder version
```

`tlds` prints each requested TLD (or the whole bootstrap map when `--tld` is
omitted) with its RDAP base URL, or `unsupported`. Run it once when adopting a new
TLD; it needs no input file and makes the §0 coverage limit visible up front.

| Flag | Default | Meaning |
|---|---|---|
| `--column` | auto | Column holding the name. Auto: first of `name,label,domain,keyword,sld`. Accepts a name or 0-based index. |
| `--tld` | — | Comma-separated TLDs (`com,io,dev`). Repeatable. Leading dots stripped. |
| `--tld-column` | — | Per-row TLDs from a column. |
| `--tld-file` | — | Newline-delimited TLD list. |
| `--has-header` | auto | `auto` sniffs; `true`/`false` force. |
| `--delimiter` | `,` | Single rune; `\t` accepted. |
| `--out` | stdout | Output path. |
| `--format` | `csv` | `csv` \| `jsonl` \| `table` \| `list` (newline-separated domains). |
| `--only` | — | Emit only these statuses, e.g. `available`. |
| `--timeout` | 10s | Per request. |
| `--retries` | 2 | Per domain. |
| `--rate` | 200ms | Minimum interval per RDAP host. |
| `--bootstrap-ttl` | 24h | RDAP bootstrap cache lifetime. |
| `--dry-run` | false | Print expanded domains and the RDAP host each would hit; no network beyond bootstrap. |
| `--log-level` | `info` | slog level; stderr always. |

Env: each flag also reads `TLDFINDER_<FLAG_UPPER_SNAKE>`; the flag wins.

Exit codes: `0` every row resolved · `1` fatal runtime error (incl. no usable
bootstrap) · `2` usage error · `3` finished with ≥1 `unknown`/`error` row ·
`130` interrupted.

Daily use is a cron or launchd line; the tool stays a one-shot. No scheduler, no
daemon.

---

## 8. Testing

| Layer | Approach |
|---|---|
| `input/name` | Table tests: IDN, uppercase, overlong labels, `xn--`, trailing dot, emoji, internal spaces, already-qualified cells. |
| `input/csv` | Golden expansion from `testdata/input/*.csv`. Header sniffing, `;` and tab delimiters, BOM, CRLF, quoted fields with commas. |
| `bootstrap` | `httptest.Server` serving a trimmed `dns.json`; assert flattening, `ETag` revalidation (304 → cache hit), stale-cache-on-failure, and hard failure with no cache. |
| `rdap` | `httptest.Server` over `testdata/rdap/*.json`; assert **every row** of §4.2's table, incl. `Retry-After` in both forms, the 5-redirect cap, and the 1 MiB body limit. |
| `run` | Injected fake HTTP transport: assert retry counts, that a 429 backs off by `Retry-After`, that pacing keys on host not TLD, and that one failing domain never aborts the run. |
| `output` | Golden CSV/JSONL byte comparison; `--only` filtering; header always present even with zero rows. |
| e2e | `-tags e2e`, off by default: real RDAP for `example.com` (registered) and a random 20-char label (available). |

CI runs `go test -race ./...`, `go vet`, `golangci-lint run`, `gofumpt -l`. No
test may touch the network without the `e2e` tag.

---

## 9. CI / build

`.github/workflows/ci.yml`: `ubuntu-latest`, Go 1.26 — build, vet, lint,
`go test -race`. `govulncheck` weekly.

`Makefile`: `build`, `test`, `lint`, `fmt`, `cover`, `clean`. Build with
`CGO_ENABLED=0`, ldflags `-s -w -X main.version=...`.

No goreleaser, no release matrix — this is one user's tool on one machine.
`go build` is the distribution mechanism.

---

## 10. Non-goals

Not in this program, and not to be added speculatively:

WHOIS fallback · web scraping of any site · TLD-List · captcha solving or
prompting · registrar pricing · registration or purchase · concurrency · a
`Checker` abstraction over one source · a resume journal · a database · a web UI ·
a daemon or built-in scheduler · proxy rotation or anti-bot evasion · trademark
screening · bulk zone-file ingestion (ICANN CZDS is the >10k/day answer, and a
different program).

---

## 11. Implementation order

One milestone; this list is the build order, each step compiling and testable.

1. `go.mod`, `Makefile`, `.golangci.yml`, CI workflow. `tldfinder version` runs.
2. `internal/input` — CSV read + expansion + IDNA/validation, with tests.
   `--dry-run` prints the expanded domain list.
3. `internal/check/bootstrap.go` — fetch, parse, cache, revalidate.
   `tldfinder tlds` runs.
4. `internal/check/rdap.go` — the query and the §4.2 status mapping, with tests.
5. `internal/check/run.go` — serial loop, pacing, retry, signals.
6. `internal/output` — CSV/JSONL/table writers, `--only`, exit codes.
7. e2e test; README usage section.

**Done when:** `tldfinder check --tld com,io,dev --only available names.csv`
resolves a 100-row CSV in under a minute, with zero `error` rows and `unknown`
rows only for TLDs `tldfinder tlds` already flagged as unsupported.
