# tldfinder

CLI that reads a CSV of candidate names and reports which `name.tld` combinations
are unregistered, using RDAP. Sized for ~100 lookups a day, one run.

The specification is [docs/PLAN.md](docs/PLAN.md).

## Usage

Build with `make build` (or `go build ./cmd/tldfinder`).

```bash
tldfinder check --tld com,io,dev --only available --out results.csv names.csv
```

```bash
tldfinder tlds --tld com,io,dev,de
```

`check` reads one column of names (auto-detected from `name,label,domain,keyword,sld`,
or `--column`), expands them against the TLD set (`--tld`, `--tld-file`, or a per-row
`--tld-column`), and writes `csv` (default), `jsonl`, or `table` via `--format`.
`--dry-run` prints the expanded domains and the RDAP host each would hit, without
checking. Every flag also reads `TLDFINDER_<FLAG_UPPER_SNAKE>`; the flag wins.
Flags must precede the input file.

Exit codes: `0` all rows resolved · `1` fatal error · `2` usage error ·
`3` finished with ≥1 `unknown`/`error` row · `130` interrupted.

Tests: `make test`; add `-tags e2e` to hit real RDAP.

## Non-obvious decisions

- **RDAP, not scraping.** Registries publish availability for free over RDAP —
  mandatory for every gTLD. No API key, no rate-limit gymnastics, no captchas.
  TLD-List, named in the original brief, is a price-comparison site behind
  Cloudflare and is not used.
- **Serial, one dependency.** 100 lookups run in ~30s without concurrency, which
  deletes the worker pool, the rate-limiter package, and the resume journal. Only
  `x/net/idna` is not stdlib.
- **`available` ≠ purchasable.** Reserved and collision-listed names are reported
  as a distinct status. RDAP carries no pricing.
- **Coverage limit:** ccTLDs missing from the IANA RDAP bootstrap come back
  `unknown`. Run `tldfinder tlds` to see which before adopting a TLD. Some (like
  `.io`) still serve RDAP unofficially — point at it with
  `--rdap-base io=https://rdap.identitydigital.services/rdap`. Others (like `.de`)
  have none at all.
- **Above ~10k lookups/day**, the right tool is an ICANN CZDS zone file, not this.

## Layout

`cmd/tldfinder` · `internal/{input,check,output}` · `docs/PLAN.md` (spec) ·
`testdata/` (captured RDAP responses + golden files).
