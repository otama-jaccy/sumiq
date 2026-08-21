# Configuration file reference

Complete schema for `sumiq`'s config files: every key, its default, and the
rules that govern it. For the narrative introduction — why the masking policy
lives in git, what `sumiq` does and doesn't protect against — see
[README.md](../README.md). For the reasoning behind each decision, see
[`docs/adr/0003-config-file-design.md`](./adr/0003-config-file-design.md) (the
schema itself), [`ADR-0007`](./adr/0007-layered-merge-guards.md) (the merge
rules and every restriction on unreviewed layers),
[`ADR-0015`](./adr/0015-datasource-default-action-monotonic-for-all-layers.md)
(monotonicity across all layers), and the ADRs those link to.

Two annotated files ship with the repo and are what `sumiq init` writes:
[`sumiq.yaml.example`](../sumiq.yaml.example) and
[`sumiq.local.yaml.example`](../sumiq.local.yaml.example). This document is the
exhaustive version of those comments.

- [File format](#file-format)
- [Layers and discovery](#layers-and-discovery)
  - [Where files are searched](#where-files-are-searched)
  - [`--config`](#--config)
- [Merge rules](#merge-rules)
- [What unreviewed layers may not write](#what-unreviewed-layers-may-not-write)
- [Schema](#schema)
  - [`version`](#version)
  - [`redash`](#redash) — including [API key](#api-key)
  - [`data_sources`](#data_sources)
  - [`query`](#query)
  - [`masking`](#masking) — [`default_action`](#default_action),
    [`rules[]`](#rules), [`propagation_exempt_functions[]`](#propagation_exempt_functions)
  - [`output`](#output)
- [Environment variables](#environment-variables)
- [Defaults](#defaults)
- [When each check runs](#when-each-check-runs)

## File format

YAML, one document per file. All layers share a single schema — there is no
"local-only" or "shared-only" set of keys, only restrictions on which values
each layer may carry (see
[What unreviewed layers may not write](#what-unreviewed-layers-may-not-write)).

Four things about loading a single file are worth knowing up front, because all
of them are deliberately strict:

- **Unknown keys are an error.** A typo like `patern:` would otherwise silently
  delete a masking rule while the run continues.
- **Multiple YAML documents are an error.** Splitting a file with `---` would
  leave the second half unread, which is the same class of accident.
- **An empty file is an error** (`設定が空です`).
- **`version` is required in every file**, including `sumiq.local.yaml` and the
  user-wide config.

Two conventions run through the whole schema:

- **A zero value means "unspecified", not "set to zero".** An omitted key is
  skipped when layers are merged, so the value from a weaker layer (or the
  built-in default) survives. Where zero is a value someone might genuinely
  intend, it is rejected rather than silently reinterpreted — see
  [`redash.timeout`](#redash) and [`query.max_rows`](#query).
- **`bool` keys are tri-state.** `auto_limit` is `*bool` internally, so
  "unspecified" is distinguishable from `false`.

## Layers and discovery

Config is resolved by layering files and then the environment. Later layers win
for scalars; lists have their own rules (see [Merge rules](#merge-rules)).

| # | Layer | Source | Reviewed? |
| - | ----- | ------ | --------- |
| 1 | built-in defaults | compiled in | — |
| 2 | user-wide | `~/.config/sumiq/config.yaml` | no |
| 3 | shared | `<repo>/sumiq.yaml` | **yes** |
| 4 | local | `<repo>/sumiq.local.yaml` | no |
| — | explicit | `--config <path>` — **replaces** layers 2–4, see [`--config`](#--config) | **yes** |
| 5 | environment | `SUMIQ_*` | no |
| 6 | flags | `--format`, `-d`, … | no |

"Reviewed" means the layer is committed to git and goes through code review.
It is the single predicate that decides whether a mask-weakening value is
allowed at all. **`~/.config/sumiq/config.yaml` is not reviewed** — it is your
own file, so it is under the same restrictions as `sumiq.local.yaml`.

### Where files are searched

- **User-wide**: `$XDG_CONFIG_HOME/sumiq/config.yaml` if `XDG_CONFIG_HOME` is
  set to a **non-empty** value, otherwise `$HOME/.config/sumiq/config.yaml`
  (an empty `XDG_CONFIG_HOME` falls through, the same "empty counts as unset"
  convention the environment layer uses). macOS uses `~/.config` too
  (not `~/Library/Application Support`), so the same path works on macOS and
  Linux. If `HOME` is unset — some CI images and containers — this layer is
  skipped rather than failing.
- **Shared and local**: searched by walking **up from the current directory to
  the git repository root**, and **every file found is layered in**, not just
  the closest one. `sumiq.yaml` and `sumiq.local.yaml` are searched
  independently, so one can sit at the repo root while the other sits in a
  subdirectory.
- Outside a git repository, only the current directory is looked at. Walking up
  without a boundary would silently pick up a `sumiq.local.yaml` left in your
  home directory.
- A path is only considered if it is a regular file (or a symlink to one), so a
  directory named `sumiq.yaml` is ignored rather than mistaken for config.
- A missing file is not an error. **The one exception is `--config`**: you named
  it, so it is loaded unconditionally and a missing file fails the run.

Loading every file up the tree — rather than the nearest one — is what keeps
masking monotonic. In a monorepo with rules at the repo root and
`packages/etl/sumiq.yaml` holding only a `timeout`, "nearest wins" would drop
every root rule when you run from `packages/etl`. Layering both makes
`masking.rules` a union and lets the nearer file override scalars only.

### `--config`

`--config <path>` **replaces the search for layers 2–4 entirely** — user-wide,
shared, and local files are all skipped, and only the named file is read on top
of the built-in defaults (the environment and flags still apply).

The explicit file is treated as **reviewed**, the same trust level as
`sumiq.yaml`, because its main use is pointing directly at a committed shared
config (`--config ./sumiq.yaml` from CI). As stated in the README, [this is not a security
boundary](../README.md#the-data-source-allowlist-is-not-a-security-boundary);
it guards against accidents, not against the person typing the path.

## Merge rules

Per-key, because "override everything" is exactly what would let local config
weaken a reviewed policy:

| Key | Rule |
| --- | ---- |
| `redash.endpoint` / `timeout` / `poll_interval` | override (later layer wins; unspecified is skipped) |
| `redash.api_key` / `api_key_command` | replaced **as a pair** by the last file read that specifies either (so: the strongest layer, and within a layer the nearest file). Both in **one file** is an error |
| `query.*` | override |
| `output.format` | override |
| `masking.rules` | **union** — append only. No layer can remove or overwrite another's rule |
| `masking.default_action` | override **only in the stricter direction**. Loosening is an error |
| `masking.propagation_exempt_functions` | union, append only. A duplicate name (case-insensitive), in the same file or across files, is an error |
| `data_sources` | keyed by `name`; entries merged **field by field**. Duplicate name within one file is an error |

`data_sources` merging field by field (rather than replacing the whole entry)
matters because the shared layer can span several files: defining `analytics`
with `default_action: redact` at the repo root and then adding only
`auto_limit: false` to the same name in `packages/etl/sumiq.yaml` must not drop
the `default_action` that was never rewritten.

When two layers define the same data source name:

- **New name** — added. If the name that wins comes from an unreviewed layer,
  **every run that uses it prints a warning** to stderr
  (`Warning: <name> はローカル定義です。マスク方針はレビューされていません。`),
  since its masking policy never went through review. It does not block the
  run.
- **Same file, twice** — error. A duplicate inside one file is a typo, and
  last-one-wins would silently delete the other.
- **Reviewed overriding unreviewed** — allowed. This is ordinary layering; if it
  were forbidden, one name in your user config could break the whole repo.
- **Unreviewed overriding reviewed** — error. Otherwise a reviewed `id` or
  `default_action` could be swapped out locally. Add it under a different name
  instead.

## What unreviewed layers may not write

Everything in this section is enforced structurally, at config load time, and
applies to both `~/.config/sumiq/config.yaml` and `sumiq.local.yaml`.

| Restriction | Why |
| ----------- | --- |
| `masking.rules[].method: none` | An explicit allow is the only way to punch a hole in an allowlist |
| `masking.rules[].method` weaker than `redact`, where the rule can apply to a `default_action: redact` scope | A matched rule always beats `default_action`, so `partial` / `hash` / `null` weaken an allowlist the same way `none` does |
| `masking.propagation_exempt_functions` | It is the only way to stop a mask from propagating |
| `data_sources[].alias_guard: off` | It is the only way to run a query whose columns can't be traced |
| Replacing a data source name defined in a reviewed layer | Would swap out a reviewed `id` or `default_action` |
| Reusing an `id` already defined in a reviewed layer under a new name | Masking strength belongs to the connection, not to the name you gave it. An alias is the same weakening as a replacement — rules scoped with `data_sources: [analytics]` match by name and would not apply to your alias |

One further restriction is often grouped with these but works differently:
**`redash.api_key` / `api_key_command` may not appear in a git-tracked file.**
That check is *not* layer-gated — it runs after the merge, over every file that
specifies either key, and a committed `sumiq.yaml` is its main target even
though `sumiq.yaml` is the reviewed layer. See [`redash`](#redash).

Two more monotonicity rules apply to **every** layer, reviewed ones included:

- `masking.default_action` can only be overridden in the stricter direction
  (`none` → `redact`). The error names the file that set the stricter value, so
  you don't have to hunt for it.
- `data_sources[].default_action` may not be looser than the global
  `masking.default_action`. Per-data-source settings are only ever applied in
  the tightening direction at mask time, so accepting a reviewed downgrade here
  would mean silently ignoring a setting someone wrote.

## Schema

### `version`

```yaml
version: 1
```

Required in every file. `1` is the only accepted value; anything else, including
omitting the key, fails the load.

### `redash`

```yaml
redash:
  endpoint: https://redash.example.com
  timeout: 300s
  poll_interval: 1s
  # api_key / api_key_command — local file or environment only
```

| Key | Type | Default | Notes |
| --- | ---- | ------- | ----- |
| `endpoint` | string | — | Required to run a query, but not required per file: it can come from any layer, so it is only checked once everything is folded together |
| `timeout` | duration | `300s` | Upper bound on waiting for a Redash job to finish |
| `poll_interval` | duration | `1s` | How often `/api/jobs/{id}` is polled |
| `api_key` | string | — | Mutually exclusive with `api_key_command` |
| `api_key_command` | list of strings | — | argv, not a shell string |

**Durations** are strings accepted by Go's `time.ParseDuration` — `300s`,
`1m30s`, `2h`. A bare number is an error (the unit is mandatory), as are
negative values and **`0s`**: zero is indistinguishable from "unspecified", so
writing it would silently leave the lower layer's value in place. Omit the key
instead.

#### API key

Three ways to provide it. The numbering below is an enumeration of the routes,
**not** a precedence order — precedence is nothing but layer precedence, so
`SUMIQ_REDASH_API_KEY` (layer 5) beats any file, and routes 2 and 3 have no
relative precedence at all because they cannot coexist in one file:

1. `SUMIQ_REDASH_API_KEY`
2. `redash.api_key` in `sumiq.local.yaml`
3. `redash.api_key_command` in `sumiq.local.yaml`

`api_key` and `api_key_command` are **mutually exclusive within one file**;
specifying both there is an error rather than a silent pick. Across files they
are replaced as a pair, so a shared `api_key_command` plus a local `api_key`
leaves only the local `api_key`. Note that "one file" is not "one layer": the
shared layer can span several files up the tree, so `api_key_command` in the
root `sumiq.yaml` and `api_key` in `packages/etl/sumiq.yaml` is not an error —
the nearer file's pair simply replaces the other.

**`redash.api_key` / `api_key_command` in a file tracked by git makes `sumiq`
refuse to start.** Specifics:

- The check runs on **every file that specifies either key**, not just the
  layer that wins. Most people pass the key by environment variable, which
  means the file loses — and checking only the winner would let a committed
  `api_key` through unnoticed.
- It is a `git ls-files` lookup, so **a brand-new file you haven't `git add`ed
  yet is not covered.** Never put a key in a shared file to begin with.
- `${env:VAR}` in a tracked file is rejected too, even though it holds no
  secret. Allowing it would make "read the value and decide whether it's safe"
  part of a reviewer's job.
- `api_key_command` is covered for a different reason: it is a command, so a
  committed one would run on `sumiq` startup for anyone who clones the repo.
- If `git` is not installed, files are treated as untracked — the accident this
  prevents can't happen without git. A `git` invocation that fails for any
  *other* reason (dubious ownership, a broken index) is an error, not a "no":
  collapsing those into "untracked" would disable the check exactly where it is
  most needed.

**`${env:VAR}` expansion** applies to `redash.api_key` read from a **file**:

- Only the exact `${env:NAME}` form is expanded. `$VAR` and other `${...}`
  shapes are left alone, because an API key may legitimately contain `$`.
- An unset variable is an error. Expanding to an empty string would surface
  later as a confusing authentication failure.
- A missing `}` or an empty name (`${env:}`) is an error.
- A key that came from `SUMIQ_REDASH_API_KEY` is **not** expanded — the syntax
  is a config-file feature, and applying it to an environment value would
  produce an error naming a `redash.api_key` you never wrote.

A local file therefore looks like this — the only keys that belong in one are
the key itself plus whatever personal data sources and extra rules you need:

```yaml
version: 1

redash:
  api_key: ${env:REDASH_API_KEY}
  # or, instead of api_key (never both in one file):
  # api_key_command: ["op", "read", "op://Private/redash/credential"]

data_sources:
  - name: my-sandbox
    id: 99

masking:
  rules:
    - patterns: ["internal_memo"]
      method: partial
      keep_prefix: 2
      keep_suffix: 2
      note: "Column that only exists in my local data."
```

**`api_key_command`** runs the argv directly (no shell) and takes its stdout as
the key, with surrounding whitespace trimmed:

- Timeout **30 seconds**, then a further 5-second grace before pipes are closed
  by force. A command that waits on interactive approval will time out; one that
  leaves a grandchild holding stdout is killed rather than hanging forever.
- Empty output is an error.
- On failure, the command's stderr is included in the error message — except
  on timeout, where the message names the command and the 30-second limit
  instead.
- It runs **lazily**, the first time the key is actually needed, and the result
  is reused. A command that only inspects config won't trigger a 1Password
  prompt.

### `data_sources`

```yaml
data_sources:
  - name: analytics
    id: 3
    description: "Production read replica."
    default_action: redact
    auto_limit: false
    alias_guard: strict
```

| Key | Type | Default | Notes |
| --- | ---- | ------- | ----- |
| `name` | string | — | **Required.** The only thing `-d` accepts |
| `id` | int | — | **Required**, must be ≥ 1. Redash's `data_source_id` |
| `description` | string | — | Free text |
| `default_action` | `none` \| `redact` | inherits global | Tightening only |
| `auto_limit` | bool | inherits `query.auto_limit` | Per-data-source override |
| `alias_guard` | `strict` \| `off` | `strict` | Reviewed layers only for `off` |

`-d` only accepts a name defined here; a numeric ID cannot be passed on the
command line, and there is no default data source. Referring to an undefined
name — from `-d` or from a rule's `data_sources` scope — is an error, not a
silently non-matching rule.

**`default_action`** overrides the global `masking.default_action` for this data
source, but only upward (`none` → `redact`). A looser value is rejected at load
time in every layer, including `sumiq.yaml`, because the masking engine would
ignore it anyway and "the setting I wrote does nothing" is worse than an error.

**`auto_limit`** exists to switch off Redash's auto-limit for data sources where
it breaks the query (Oracle, SQL Server). See [`query`](#query).

**`alias_guard`** controls what happens when `sumiq` cannot trace a result
column back to the columns it derives from. `strict` (the default) refuses to
run and **never sends a request to Redash**; `off` runs anyway, keeps
propagation for whatever *was* analyzed, and prints a warning on every run.
There is deliberately **no global setting** — whether a query language can be
read at all is a property of the data source itself. Non-SQL query runners need
`alias_guard: off`. The full list of situations that trip the guard is in the
README section on
[`alias_guard`](../README.md#alias_guard--queries-whose-columns-cant-be-traced).

### `query`

```yaml
query:
  auto_limit: true
  max_rows: 1000
  on_exceed: error
```

| Key | Type | Default | Notes |
| --- | ---- | ------- | ----- |
| `auto_limit` | bool | `true` | Optimization only. **Not** a safety net |
| `max_rows` | int | `1000` | Client-side row cap — the real safety net |
| `on_exceed` | `error` \| `truncate` | `error` | What happens past `max_rows` |

`auto_limit` asks Redash to apply its own "LIMIT 1000" equivalent. It silently
does nothing for queries containing CTEs and for non-SQL data sources, and on
Oracle / SQL Server it breaks the query outright — set `auto_limit: false` for
those individually. Be aware that whenever the effective `auto_limit` is
`false`, **every run prints a warning** to stderr noting that Redash applied no
row limit of its own and the rows fetched may have been transferred in full.
That warning is suppressed when `max_rows` was exceeded, since the truncation
notice or error takes precedence.

`max_rows` is enforced client-side after fetching, so it applies even where
`auto_limit` doesn't. It is also passed down to the fetch itself, so a huge
result set can't exhaust memory before the check is reached.

- **A negative `max_rows` is an error.** `0` is not rejected in a file, because
  it can't be told apart from "unspecified" — it leaves the default of `1000`
  in place. From the environment, where the intent is unambiguous, `0` *is*
  rejected (see [Environment variables](#environment-variables)).
- **`auto_limit: true` with `max_rows` above 1000 is an error.** Redash's
  auto-limit is hard-coded to 1000 rows and cannot be raised through the API,
  so a higher `max_rows` would be unreachable. Lower `max_rows`, or set
  `auto_limit: false`. The check uses the *effective* `auto_limit` for the data
  source being queried, so a data source with `auto_limit: false` is unaffected.
- `on_exceed: truncate` can lead to mistaking a partial result for a complete
  one. Opt into it only where you need it.

### `masking`

```yaml
masking:
  default_action: none
  propagation_exempt_functions:
    - name: count
      note: "Only a row count comes out, never the value itself."
  rules:
    - patterns: ["*email*"]
      method: partial
      keep: domain
      note: "Keep only the domain."
      data_sources: [analytics]
```

#### `default_action`

`none` (default) masks only columns that match a rule — a denylist. `redact`
masks unmatched columns too — an allowlist. The global default is the loosest
value on purpose: overrides only work in the tightening direction, so a stricter
global would leave no room to choose.

#### `rules[]`

| Key | Type | Notes |
| --- | ---- | ----- |
| `patterns` | list of strings | **Required**, non-empty. Glob by default, `regex:` prefix for regular expressions |
| `method` | `none` \| `partial` \| `hash` \| `null` \| `redact` \| `drop` | **Required** |
| `keep` | `domain` | `partial` only |
| `keep_prefix` | int | `partial` only, non-negative |
| `keep_suffix` | int | `partial` only, non-negative |
| `note` | string | Why this column is hidden. Free text, for reviewers |
| `data_sources` | list of names | Scope. Omitted means every data source |

**Method strength**, used when several rules match one column:

```
drop > redact > null > hash > partial > none
```

The strongest wins. A matched rule always beats `default_action`, which is what
makes `method: none` work as an allowlist hole — the "strongest wins" rule
applies between matching rules, never between a rule and the default.

**`method: "null"` must be quoted.** Unquoted `null` is YAML's null literal, so
the key parses as empty and you get a confusing "method not specified" error.

**Pattern dialect:**

| | Glob (default) | `regex:` prefix |
| - | -------------- | --------------- |
| Match | **whole column name** | **substring** |
| Case | insensitive | insensitive (`(?-i)` to restore) |
| Metacharacters | `*` (any run of **zero or more** characters, including `/` and newlines), `?` (any single character) | Go `regexp` syntax |
| `[` | **error** | ordinary regex syntax |

Everything except `*` and `?` matches literally, so a pattern that is just a
column name always matches that column whatever characters it contains. `[` is
rejected rather than guessed at: read as a character class or as a literal, one
of the two readings drops a mask. Go's `path.Match` is deliberately not used —
its `/` handling and backslash escaping (which differs again on Windows) would
each make masks silently miss.

The whole-match/substring asymmetry matters most for `method: none`:
`regex:user_id` would also let through `user_identity` and `user_id_hash`.
**`regex:` is therefore rejected outright with `method: none`** — list the
columns you are allowing with globs, one by one.

Because globs are whole-name matches, `"*tel"` will not match `tel_no`; use
`"*tel*"`. Being too broad only over-masks, which is the safe direction.

**`partial` and the `keep` options.** Each `keep` option states a condition
under which part of the value may survive; whatever no option keeps is replaced
by `****`. So a value that fails `keep: domain` is hidden entirely unless a
`keep_prefix` / `keep_suffix` on the same rule still applies to it
(`keep: domain` plus `keep_prefix: 3` gives `not****` for `notanemail`).

- `keep: domain` keeps the part after the **last** `@`, **but only if that part
  looks like a hostname** — two or more dot-separated labels of alphanumerics,
  `-`, and `_`. `user@example.com` → `****@example.com`, while `notanemail`,
  `@taro_handle`, `user@localhost`, and any value with non-hostname text after
  the address (`"contact bob@example.com now"`) become `****`. What decides is
  that tail, not whether the column holds free text: a value whose address sits
  at the very end (`"連絡先 bob@example.com"`) does keep its domain, though
  everything before it is still hidden. A pattern like `*mail*` also hits
  `mail_body`; keeping everything after `@` unconditionally would dump the body
  of a free-text column. The cost of requiring a dot is that TLD-less addresses
  lose their domain.
- `keep_prefix` / `keep_suffix` count **runes**, not bytes. If the amount to
  keep is greater than or equal to the value's length, the whole value is
  hidden: `keep_prefix: 4` against `"abc"` gives `****`, not `abc`. The mask is
  always exactly `****` regardless of length, so length itself isn't leaked.
- **Several matching `partial` rules intersect**, they don't compete:
  `keep_prefix` and `keep_suffix` each take the smaller value, and
  `keep: domain` applies only if every matching rule asked for it. Adding a rule can therefore never reveal
  more.
- **`method: partial` with no `keep` option is an error** (it would be identical
  to `redact`, so it is more likely a mistake), and **a `keep` option on any
  other method is an error** — `method: none` with `keep_prefix: 3` would
  otherwise pass the column through untouched while reading as "show 3
  characters".

**`hash`** replaces the value with the first 12 characters of
`sha256(salt + value)`, using a **salt generated randomly per run**. Hashes are
consistent within one run (so you can count and join on them) and deliberately
not comparable across runs — a fixed salt would make low-cardinality values
recoverable by brute force.

**`note`** has no effect on behavior. It is the field that makes a masking rule
reviewable, which is the point of keeping the policy in git.

#### `propagation_exempt_functions[]`

Rules match on column names, and column names come from the query — so
`SELECT email AS contact` would escape a rule on `email`. `sumiq` therefore
reads the SQL, collects the names each result column **may derive from**, and
applies the strongest mask among them. This list is the only way to stop that
propagation.

| Key | Type | Notes |
| --- | ---- | ----- |
| `name` | string | **Required.** Exact, case-insensitive match |
| `note` | string | Why this function can't emit the original value |

- **Empty by default — not even `count` is built in.** A weakening that isn't
  written down can't be traced from the config when something does leak.
- **Reviewed layers only**, like `method: none`.
- **Duplicate names are an error**, in one file or across files. Stacking them
  silently would leave one `note` explaining a rule while the other is what
  applies.
- Matching is exact and case-insensitive. **Globs, `regex:`, parentheses,
  whitespace, and schema qualification are all rejected at load time** rather
  than accepted as something that matches nothing — `count*` would let through a
  user-defined `count_raw_emails`, and a schema-qualified call such as
  `pg_catalog.count` doesn't stop propagation anyway. Names must otherwise look
  like identifiers: letters and `_`, with digits allowed after the first
  character.
- **Be careful with `min` / `max` / `sum` / `avg`.** For a small group they
  return the original value (with one row, `sum` *is* the value). `sumiq` can't
  decide which functions are safe; that is a team judgment, which is why the
  list is explicit and reviewed.
- **It only stops propagation.** Name matching and `default_action` still apply:
  under `default_action: redact`, `count(email) AS n` is still `****` unless you
  also open a `method: none` hole for `n`. And only identifiers *inside* the
  exempt call are stopped — `count(email) OVER (PARTITION BY email)` still
  propagates through the second `email`.
- Whenever an exemption actually stops a mask, it is reported on stderr.

The propagation analysis is deliberately **over-approximate**: without a schema
catalog, exact column lineage is unavailable (`*` can't be expanded), so
unrelated names — a table name, a type inside a `CAST` — can end up counted as
sources, and scopes are flattened so same-named aliases in different subqueries
are treated as one. The only consequence is masking more than necessary. It
also does not stop deliberate circumvention: anyone who can build values
dynamically can defeat it.

### `output`

```yaml
output:
  format: table
```

| Key | Type | Default |
| --- | ---- | ------- |
| `format` | `table` \| `json` \| `csv` | `table` |

**Known limitation: this key is currently not consumed.** `sumiq query` and
`sumiq data-sources list` both take `--format`, which carries its own default of
`table` and is passed straight through, so the flag always decides the output
format and neither `output.format` nor `SUMIQ_OUTPUT_FORMAT` changes it. Both
are parsed and validated (an invalid value still fails the load), they just
don't reach the renderer yet. Pass `--format` explicitly for now.

## Environment variables

Layer 5. Values are validated exactly as their file counterparts are, and the
environment layer is folded in through the same merge rules — so, for instance,
`SUMIQ_MASKING_DEFAULT_ACTION=none` cannot loosen a `redact` set in
`sumiq.yaml`. Giving the environment its own merge path would be a hole.

| Variable | Sets | Accepted values |
| -------- | ---- | --------------- |
| `SUMIQ_REDASH_ENDPOINT` | `redash.endpoint` | any string |
| `SUMIQ_REDASH_API_KEY` | `redash.api_key` | any string; **no `${env:}` expansion** |
| `SUMIQ_REDASH_TIMEOUT` | `redash.timeout` | duration, non-zero |
| `SUMIQ_REDASH_POLL_INTERVAL` | `redash.poll_interval` | duration, non-zero |
| `SUMIQ_QUERY_AUTO_LIMIT` | `query.auto_limit` | `true` / `false` (Go `ParseBool`, so `1` / `0` / `T` / `F` also work) |
| `SUMIQ_QUERY_MAX_ROWS` | `query.max_rows` | integer ≥ 1 |
| `SUMIQ_QUERY_ON_EXCEED` | `query.on_exceed` | `error` / `truncate` |
| `SUMIQ_MASKING_DEFAULT_ACTION` | `masking.default_action` | `none` / `redact` (tightening only) |
| `SUMIQ_OUTPUT_FORMAT` | `output.format` | `table` / `json` / `csv` (see the limitation above) |

Also read, for discovery rather than config: `XDG_CONFIG_HOME` and `HOME`.

- **An empty value counts as unset**, since it can't be told apart from a
  variable someone forgot to clear.
- **`0` is rejected** for `SUMIQ_QUERY_MAX_ROWS` and for both durations. Unlike
  a file, there is no ambiguity about intent here, and falling back to the
  default would hand you a value you didn't ask for.
- **Lists cannot be set from the environment.** There is no encoding for
  `data_sources` or `masking.rules`, by design: a syntax for cramming a masking
  rule into a shell variable would be a route around review.
- Variable names are maintained as an explicit table in the source, not derived
  by reflection — environment variable names end up in shell history and CI
  configuration, so they must not change just because a struct field was
  renamed.

## Defaults

The full built-in layer:

```yaml
version: 1
redash:
  timeout: 300s
  poll_interval: 1s
query:
  auto_limit: true
  max_rows: 1000
  on_exceed: error
masking:
  default_action: none
output:
  format: table
```

`redash.endpoint`, `data_sources`, and `masking.rules` have no defaults.
`alias_guard` defaults to `strict` per data source and has no global setting.

## When each check runs

Useful when reading an error message: the layer named in the message tells you
which stage rejected the config.

| Stage | Checks |
| ----- | ------ |
| **Per file** | unknown keys, multiple documents, empty file, `version`, duration syntax, negative `max_rows`, `data_sources[]` `name`/`id`, non-empty `patterns`, present `method`, exempt-function name syntax |
| **Merge** | `default_action` monotonicity, `method: none` / exempt functions / `alias_guard: off` restricted to reviewed layers, duplicate exempt names, data source name and `id` collisions, per-data-source `default_action` vs. global, rules weaker than `redact` in a `redact` scope, `api_key` + `api_key_command` in one file |
| **After merge** | `api_key` / `api_key_command` in a git-tracked file — always, regardless of which layer wins |
| **Before running a query** | `endpoint` present, API key resolvable, `auto_limit` vs. `max_rows` reachability, every rule's patterns and `keep` options compile, rule `data_sources` names exist, `regex:` not used with `method: none`, and — under `alias_guard: strict` — that the query's columns can be traced |

Everything in the last row happens **before any request reaches Redash**, so a
config mistake fails fast instead of after a long-running job.
