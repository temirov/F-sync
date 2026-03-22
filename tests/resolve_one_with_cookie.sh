#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "Usage: $0 <numeric_id>" >&2
  exit 2
fi

ID="$1"

# Paste your Cookie header value between the quotes below (keep it private)
# Example: COOKIE='guest_id=...; ct0=...; auth_token=...;'
COOKIE=''
if [ -z "$COOKIE" ]; then
  read -rp "Paste Cookie header for x.com (keep private): " COOKIE
fi

UA='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36'
ACCEPT='text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8'
HEADERS="-H \"User-Agent: $UA\" -H \"Accept: $ACCEPT\" -H \"Cookie: $COOKIE\" -i"

echo "Trying x.com..."
# Use HTTP/1.1 and GET so Cloudflare sees it like a browser
curl --http1.1 -s -D - -o /dev/null -H "User-Agent: $UA" -H "Accept: $ACCEPT" -H "Cookie: $COOKIE" "https://x.com/i/user/$ID" | sed -n '1,120p'

echo
echo "Extracted Location (if any):"
curl --http1.1 -s -D - -o /dev/null -H "User-Agent: $UA" -H "Accept: $ACCEPT" -H "Cookie: $COOKIE" "https://x.com/i/user/$ID" \
  | awk -F': ' 'tolower($1)=="location"{print $2}' | tr -d '\r' || true

# fallback to twitter.com if needed
echo; echo "Fallback: twitter.com"
curl --http1.1 -s -D - -o /dev/null -H "User-Agent: $UA" -H "Accept: $ACCEPT" -H "Cookie: $COOKIE" "https://twitter.com/i/user/$ID" | sed -n '1,120p'
