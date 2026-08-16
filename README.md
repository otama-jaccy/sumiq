# sumiq

A CLI that runs ad-hoc queries against Redash and prints the results through
a masking policy you define.

Instead of dumping raw SQL results straight to a screen or a CI log, `sumiq`
passes them through a masking policy (`sumiq.yaml`) that your team reviews
ahead of time. That policy lives in git, so it's reviewable like any other
code change.

**`sumiq` is not a security boundary.** Anyone with the underlying Redash API
key can bypass it entirely by calling the API directly. Its purpose is
accident prevention and making the masking policy reviewable — not access
control.

## Features

- Runs a query against a named Redash data source and prints masked results
- Masking rules are declared in a config file, reviewed in git, and applied
  consistently across every invocation
- Layered config (shared + local + env + flags) so personal setup never
  weakens a team's reviewed masking policy
- `table` / `json` / `csv` output, with masking/truncation info reported
  separately on stderr so stdout stays pipeable
- A real row cap (`max_rows`) that's enforced client-side, so it still
  applies even when Redash's own query-level limit doesn't

## Installation

```bash
go install github.com/otama-jaccy/sumiq/cmd/sumiq@latest
```

Or clone and build it yourself:

```bash
git clone https://github.com/otama-jaccy/sumiq.git
cd sumiq
go build -o sumiq ./cmd/sumiq
```

## Quick start

1. Set up config files (see [Configuration](#configuration) below; the
   fastest way is to just copy the two examples):

   ```bash
   cp sumiq.yaml.example sumiq.yaml
   cp sumiq.local.yaml.example sumiq.local.yaml
   ```

2. Provide your Redash API key (see [API key](#api-key) below; the fastest
   way is an environment variable):

   ```bash
   export SUMIQ_REDASH_API_KEY=xxxxxxxx
   ```

3. Run a query:

   ```bash
   sumiq query -d analytics "SELECT id, email FROM users LIMIT 10"
   ```

   - `-d` / `--data-source` is required. It only accepts a name defined in
     your config — you can't pass a numeric data source ID directly, and
     there is no default.
   - `--format` selects `table` (default), `json`, or `csv`.

   ```bash
   sumiq query -d analytics --format json "SELECT id, email, memo FROM users" | jq .
   ```

Masking and truncation info is printed to stderr. stdout carries only the
data, so it stays safe to pipe:

```
Masked: email (partial), memo (redact)
Dropped: --
Rows: 342
```

## Configuration

Config is resolved by layering multiple files together (later layers win;
scalars are overridden, not merged):

```
1. built-in defaults
2. ~/.config/sumiq/config.yaml   user-wide
3. <repo>/sumiq.yaml             shared, committed to the repo
4. <repo>/sumiq.local.yaml       local, gitignored
5. SUMIQ_* environment variables
6. command-line flags
```

Layers 3 and 4 are searched by walking up from the current directory to the
git repository root, and **every file found is layered in** — not just the
closest one. In a monorepo with `sumiq.yaml` at both the repo root and
`packages/etl/`, both are loaded and their masking rules are unioned.
Passing `--config` explicitly skips this search (layers 2–4) entirely.

Copy the two example files to get started (see [Quick start](#quick-start)):

- **`sumiq.yaml`** (shared config) — your Redash endpoint, data source
  definitions, and masking rules. Committed to git and reviewed by your team.
- **`sumiq.local.yaml`** (local config) — your API key, plus any personal
  data sources or extra masking rules that only apply on your own machine.

### Add `sumiq.local.yaml` to `.gitignore`

**Required.** This repo's own `.gitignore` already lists `sumiq.local.yaml`
(without a leading `/`, so that local configs placed in subdirectories are
also excluded — see the search behavior above). If you're reusing this setup
in a fork or a different repo, add the same line to your own `.gitignore`.

### API key

Writing `redash.api_key` / `redash.api_key_command` into a file **tracked by
git** (typically `sumiq.yaml`) makes `sumiq` refuse to start at load time.
This isn't a "please don't do this" convention — it's enforced structurally.

**That check works by asking `git ls-files` whether the file is tracked, so
a brand-new file you haven't `git add`ed yet is not covered.** Creating a
fresh `sumiq.yaml` and writing an API key into it to try things out will not
be caught by this check. Never put an API key in a shared file in the first
place.

There are three ways to provide the API key (see `sumiq.local.yaml.example`
for details):

1. The `SUMIQ_REDASH_API_KEY` environment variable
2. `redash.api_key` in `sumiq.local.yaml` (supports `${env:VAR}` expansion)
3. `redash.api_key_command` in `sumiq.local.yaml` — stdout of an external
   command (e.g. the 1Password CLI)

`api_key` and `api_key_command` are mutually exclusive.

### Masking can't be weakened from local config

Masking is a safety mechanism, and the structure specifically prevents
`sumiq.local.yaml` from weakening what `sumiq.yaml` enforces.

- Rules apply as the **union** across all layers (local only adds rules, it
  never removes or overrides shared ones).
- If multiple rules match the same column, **the strongest method wins**
  (`drop > redact > null > hash > partial > none`).
- Overriding `default_action` (how unmatched columns are treated) is only
  ever allowed to make it **stricter**, never looser.
- **`method: none` (an explicit allow) can only be written in the shared file
  (`sumiq.yaml`).** It's meant for allowlist setups (`default_action:
  redact`) where specific columns are deliberately let through — allowing it
  from local config would itself be a way to weaken the policy.
- A rule can be scoped to specific data sources with `data_sources: [name,
  ...]`; omitting it applies the rule to every data source.

Column-name patterns are glob by default (`*` matches any run of characters,
`?` matches a single character, matching is case-insensitive). Patterns
containing `[` are rejected as an error. To match part of a column name, use
the `regex:` prefix instead (also case-insensitive).

### `hash` uses a random salt per run — values don't match across runs

`hash` replaces a value with the first 12 characters of
`sha256(salt + value)`. **The salt is generated randomly for each run.** It's
consistent within a single run, so you can count or join on hashed values
within that run's output. But **a hash from one run cannot be matched
against a hash from a different run**, even if the underlying value is the
same. This is intentional — a fixed or shared salt would make low-cardinality
values recoverable by brute force, and there's no plan to change that.

### `auto_limit` isn't reliable — `max_rows` is the actual safety net

`query.auto_limit` (default `true`) is just an **optimization** that invokes
Redash's own "LIMIT 1000"-equivalent feature. It **silently does nothing** for
queries containing CTEs or for non-SQL data sources (and on Oracle / SQL
Server it actually breaks the query, so set `auto_limit: false` for those
individually).

**The safety net that actually works is `query.max_rows`.** It's enforced
client-side after fetching, so it applies even in cases where `auto_limit`
doesn't. What happens when it's exceeded is controlled by `query.on_exceed`
(default `error`). `truncate` can lead to mistaking a partial result for the
full result set, so opt into it explicitly only when you need it.

### The data source allowlist is not a security boundary

Restricting `-d` to the data source names defined in config exists to catch
mistakes and make the reviewed scope explicit — **it is not access control.**
Anyone with the underlying Redash API key can bypass it entirely with `curl`
or similar. Likewise, running against a locally-defined data source (one
added under `sumiq.local.yaml`'s `data_sources`) prints a warning every time,
since its masking policy hasn't gone through team review — but it doesn't
block the run.

## Output formats

`--format table` (default) / `json` / `csv`.

| method    | table            | json                 | csv         |
| --------- | ---------------- | -------------------- | ----------- |
| `redact`  | `****`           | `"****"`             | `****`      |
| `partial` | kept portion     | same (as a string)   | same        |
| `hash`    | first 12 hex chars | same (as a string) | same        |
| `null`    | `NULL`            | `null`               | empty field |
| `drop`    | column omitted    | key omitted          | omitted from header too |

Values masked by `redact` / `partial` / `hash` are always output as strings
(the original type is not preserved). **`null` is the exception: in JSON it
becomes a real JSON `null`, not the string `"null"`.** Unmasked columns keep
their original type.

**`method: null` collides with YAML's bare `null` literal, so write it
quoted as `method: "null"` in config files.** Forgetting the quotes makes it
parse as an empty value, which surfaces as a confusing "method not
specified" error.

### `csv` cannot distinguish `null` from an empty string

This is a limitation of the CSV format itself, and `sumiq` doesn't work
around it. If you need to tell a `NULL` value apart from an empty string,
use `json` output instead.

## License

[MIT](LICENSE)
