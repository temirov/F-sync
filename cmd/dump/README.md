# Twitter Relationship Matrix

Generate an offline, human-readable HTML “relationship matrix” by comparing two X/Twitter data-export archives. See at a
glance who is a **Friend** (mutual), **Leader** (you follow them, they don’t follow you), **Groupie** (they follow you,
you don’t follow them), plus **diffs** (who B follows that A doesn’t) and **blocked/muted** cross-matches. Every run
launches headless Chrome to resolve missing handles by following redirects on twitter.com, so a Chrome/Chromium binary
(exposed via `PATH` or `CHROME_BIN`) and outbound network access are mandatory.

---

## Why this exists

* **Mostly local, privacy-aware**: Works on your exported data (`.zip`) and only reaches out to twitter.com to fill
  missing handles using short-lived headless Chrome sessions.
* **Actionable overview**: Summaries + categorized lists with direct profile/intent links.
* **Deterministic**: Plain Go code, no heuristics beyond consistent parsing rules.

---

## What it produces

One self-contained HTML file (default `twitter_relationship_matrix.html`) with:

1. **Overview** (per owner): totals for Followers, Following, Friends, Leaders, Groupies, Muted, Blocked.
2. **Relationship Matrix** (per owner):

    * **Friends** — mutual follow
    * **Leaders** — you follow, they don’t follow back
    * **Groupies** — they follow you, you don’t follow back
3. **Diff**: “B follows that A doesn’t” — a quick way to clone follow lists.
4. **Blocked sections** (per owner): Blocked ∩ Following, Blocked ∩ Followers, all Blocked.
5. **Badges**:

    * `ID only` when the export lacks a handle/display name
    * `Muted` when present in `mute.js`
    * `Blocked` when present in `block.js`

Numbers and names are clickable; “Follow” buttons use the standard Twitter intent URLs.

---

## Inputs

Two official X/Twitter **data export ZIPs** (e.g., the kind you request from your account). The program parses:

* `manifest.js` — owner identity and exact file paths
* `following.js` — accounts the owner follows
* `follower.js` — accounts following the owner
* `mute.js` — muted account list
* `block.js` — blocked account list

The parser tolerates typical “JS wrapper + JSON” formats in exports.

---

## How it works (architecture)

### High-level flow

1. **Load archives** (two ZIPs):

    * Read `manifest.js` to discover canonical paths of data files.
    * Lazily extract `following.js`, `follower.js`, `mute.js`, `block.js` (fallback by basename if needed).
2. **Normalize records**:

    * Each account is `AccountRecord{AccountID, UserName, DisplayName}`.
    * If `accountId` is missing but a `userLink` exists, extract the numeric ID via regex.
3. **Classify relationships** (per owner):

    * **Friends**: `Following ∩ Followers`
    * **Leaders**: `Following − Followers`
    * **Groupies**: `Followers − Following`
4. **Compute diffs**: accounts that **B follows** and **A does not**.
5. **Resolve labels for blocked lists**: prefer labels from the owner’s Following/Followers; otherwise borrow from the
   other archive; otherwise show numeric ID.
6. **Render HTML**:

    * Pure string builder, no templating engine.
    * Styles come from `cssBase.css` (embedded via `//go:embed`).

### Principles

* **Use official export data only** — no scraping or live API calls.
* **Conservative parsing** — tolerate minor export formatting differences.
* **Deterministic output** — sorted, stable ordering (display name → handle → ID).
* **Separation of concerns** — logic in Go, presentation in a small embedded CSS file.
* **Legibility over cleverness** — descriptive identifiers; avoid single-letter variables.

---

## Build & run

### Requirements

* Go 1.21+ (or recent)
* Two X/Twitter export ZIPs (for “Account A” and “Account B”)
* Google Chrome or Chromium installed and discoverable via `PATH` or the `CHROME_BIN` environment variable (the CLI exits
  early when the browser is missing)
* Outbound HTTPS access to `https://twitter.com` and `https://x.com` so the resolver can follow redirects for missing
  handles

### Build

```bash
go build -o twitter_matrix .
```

### Run

```bash
./twitter_matrix \
  --zip-a /path/to/accountA_export.zip \
  --zip-b /path/to/accountB_export.zip \
  --out twitter_relationship_matrix.html
```

* `--zip-a` Path to the first export ZIP (treated as **A**)
* `--zip-b` Path to the second export ZIP (treated as **B**)
* `--out`  Output HTML file (default: `twitter_relationship_matrix.html`)

Open the resulting HTML in your browser.

---

### Handle resolution (built-in)

Some exports omit screen names for deactivated or protected accounts. The CLI resolves those gaps on every invocation:
it performs HTTPS HEAD/GET requests to `https://twitter.com/i/user/<account_id>` and inspects the redirect to recover
the handle. It then fetches the redirected page title to extract a display name when available. When individual lookups
fail, the program prints warnings to `stderr` and continues rendering with numeric IDs. Because the resolver is always
on, missing prerequisites (no Chrome, blocked network access, etc.) cause the CLI to exit with a fatal error before
rendering output.

* Requests use conservative timeouts and never follow more than the initial redirect.
* A bounded worker pool (default 8 workers) fans out requests so large exports finish promptly without hammering
  twitter.com.
* Results are cached for the lifetime of the process, so repeated IDs are only fetched once.

---

## UI notes & customization

* **Tiny matrix numbers**: IDs in matrix lists are intentionally small to fit many rows.
  Tweak in `cssBase.css`:

  ```css
  .matrix li { font-size:10px; line-height:1.2; }
  ```
* **Responsive layout**: On narrow screens, the grid collapses to a single column.
* **Badges & buttons**: Adjust `.badge`, `.btn` rules in `cssBase.css` to taste.

All CSS is embedded at build time via:

```go
//go:embed base.css
var cssBase string
```

So edits to `base.css` are picked up on the next build.

---

## Edge cases & troubleshooting

* **“no follower.js or following.js found in zip”**
  The export may be incomplete or in a different layout. Re-request the data export from X/Twitter and ensure the ZIP
  contains the files listed above (paths are discovered via `manifest.js`).
* **Many “ID only” badges**
  Some exports omit handles/display names in certain records. The tool falls back to numeric IDs; clicking still opens
  the correct profile.
* **Very large lists**
  Rendering is plain HTML; modern browsers handle thousands of rows fine, but you can reduce font size further or narrow
  sections in CSS.
* **Cross-account labeling**
  Blocked lists try to borrow labels from both archives for better readability. If neither has a label, numeric ID is
  shown.

---

## Security & privacy

* Operates on local files and performs outbound HTTPS lookups to twitter.com for missing handle lookups.
* Does not write anything except the single HTML output file.
* No token storage, no persistent cache.

---

## File layout (key files)

```
.
├─ main.go          # entry point: flag parsing, reading zips, rendering
├─ cssBase.css      # small stylesheet, embedded via //go:embed
```

---

## Flags (quick reference)

| Flag      | Type   | Required | Description                             |
|-----------|--------|----------|-----------------------------------------|
| `--zip-a` | string | Yes      | Path to Account A export ZIP            |
| `--zip-b` | string | Yes      | Path to Account B export ZIP            |
| `--out`   | string | No       | Output HTML path (default: shown above) |

---

## Roadmap ideas

* Export CSVs per bucket (Friends/Leaders/Groupies/Diffs).
* Toggle sections on/off in UI.
* Optional JSON output for automation.
