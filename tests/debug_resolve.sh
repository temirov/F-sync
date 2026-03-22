#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "Usage: $0 <numeric_id>" >&2
  exit 2
fi

ID="$1"
case "$ID" in ''|*[!0-9]*)
  echo "error: not a numeric id: $ID" >&2; exit 2 ;;
esac

UA='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36'
AL='en-US,en;q=0.9'
ACCEPT='text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8'
COOKIE_JAR="$(mktemp -t x_cookie_XXXXXX.jar)"

# temp files (no $domain in the trap!)
HDR_X="$(mktemp -t x_hdr_xcom_XXXXXX)"
HDR_TW="$(mktemp -t x_hdr_tw_XXXXXX)"
BODY_X="$(mktemp -t x_body_xcom_XXXXXX)"
BODY_TW="$(mktemp -t x_body_tw_XXXXXX)"

cleanup() {
  rm -f "$COOKIE_JAR" "$HDR_X" "$HDR_TW" "$BODY_X" "$BODY_TW" || true
}
trap cleanup EXIT

echo ">>> Priming guest cookies..." >&2
curl --http1.1 -s -c "$COOKIE_JAR" \
  -H "User-Agent: $UA" -H "Accept: $ACCEPT" -H "Accept-Language: $AL" \
  https://x.com/ >/dev/null || true

fetch_and_dump() {
  local domain="$1" hdr="$2" body="$3"
  local url="https://${domain}/i/user/${ID}"

  echo
  echo "===== ${domain} ====="
  echo "GET $url"
  echo "------------------- RAW (headers first, then first 1000 bytes of body) -------------------"

  # -D writes headers to $hdr, -o writes body to $body
  curl --http1.1 -s -D "$hdr" -o "$body" -b "$COOKIE_JAR" \
    -H "User-Agent: $UA" -H "Accept: $ACCEPT" -H "Accept-Language: $AL" \
    "$url" || true

  local status location retry_after
  status="$(head -n1 "$hdr" | tr -d '\r' | awk '{print $2}')"
  location="$(awk -F': ' 'tolower($1)=="location"{print $2}' "$hdr" | tr -d '\r' | head -n1)"
  retry_after="$(awk -F': ' 'tolower($1)=="retry-after"{print $2}' "$hdr" | tr -d '\r' | head -n1)"

  echo "HTTP status: ${status:-<none>}"
  echo "Location: ${location:-<none>}"
  [ -n "${retry_after:-}" ] && echo "Retry-After: $retry_after"

  echo
  echo "--- HEADERS ---"
  sed 's/\r$//' "$hdr"

  echo
  echo "--- BODY (first 1000 chars) ---"
  head -c 1000 "$body" | sed -e 's/\x0/\n/g'

  echo
  echo "--- curl -L effective URL (if server actually redirected with 3xx) ---"
  curl --http1.1 -Ls -o /dev/null -w '%{url_effective}\n' "$url" || true
}

fetch_and_dump "x.com"       "$HDR_X" "$BODY_X"
fetch_and_dump "twitter.com" "$HDR_TW" "$BODY_TW"

echo
echo "===== INTERPRETATION ====="
cat <<'EOF'
• If HTTP status is 3xx and "Location" is present → the last path segment of Location is the handle.
• If HTTP status is 200 with a large HTML/JS body (and cookies like __cf_bm) → CF/JS wall (no redirect).
• If HTTP status is 403/429 → challenged/rate-limited; retry later or from different egress.
• The “effective URL” only shows a value if a real 3xx redirect occurred (not JS-driven nav).
EOF
