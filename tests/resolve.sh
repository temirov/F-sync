#!/usr/bin/env bash
# Resolve a numeric X/Twitter user ID to both a handle and display name.
# Each candidate URL is rendered once via headless Chrome; the cached DOM is reused to
# derive both fields. The script now fails unless both the handle and the normalized
# display name are non-empty so whitespace-only results are rejected.

ID=156576788
CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
RESERVED_HANDLE_PATTERN='^(home|tos|privacy|explore|notifications|settings|i|intent|login|signup|share|account|compose|messages|search)$'

for url in "https://x.com/intent/user?user_id=$ID" "https://x.com/i/user/$ID"; do
  dom=$(
    "$CHROME_BIN" --headless=new --disable-gpu --use-gl=swiftshader --enable-unsafe-swiftshader \
      --hide-scrollbars --no-first-run --no-default-browser-check --log-level=3 --silent --disable-logging \
      --virtual-time-budget=15000 --dump-dom "$url" 2>/dev/null
  ) || continue

  if [ -z "$dom" ]; then
    continue
  fi

  normalized_dom=$(printf '%s' "$dom" | tr "'" '"')

  handle=$(printf '%s\n' "$normalized_dom" \
    | grep -Eo 'https://(x|twitter)\.com/[A-Za-z0-9_]{1,15}' \
    | sed -E 's#https?://(x|twitter)\.com/##' \
    | grep -Evi "$RESERVED_HANDLE_PATTERN" \
    | head -n1)

  display=$(printf '%s\n' "$normalized_dom" \
    | grep -Eo 'property="og:title"[^>]*content="[^"]+"' \
    | head -n1 \
    | sed -E 's/.*content="([^"]+)".*/\1/')

  if [ -z "$display" ]; then
    display=$(printf '%s\n' "$normalized_dom" \
      | grep -Eo '<title[^>]*>[^<]+</title>' \
      | head -n1 \
      | sed -E 's/.*>([^<]+)<.*/\1/')
  fi

  if [ -n "$display" ]; then
    display_cleanup=$display

    if [ -n "$handle" ]; then
      display_cleanup=$(printf '%s' "$display_cleanup" | sed -E "s/[[:space:]]*\(@${handle}\)//")
    fi

    display_cleanup=$(printf '%s' "$display_cleanup" | sed -E 's/[[:space:]]+\/ X$//; s/[[:space:]]+on X$//')
    display=$(printf '%s' "$display_cleanup" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')
  fi

  if [ -n "$handle" ] && [ -n "$display" ]; then
    printf 'Handle: %s (retrieved from %s)\n' "$handle" "$url"
    printf 'Display name: %s\n' "$display"
    exit 0
  fi

done

echo "Failed to resolve both handle and display name for ID ${ID}" >&2
exit 1
