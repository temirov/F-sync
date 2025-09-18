ID=156576788
CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
for url in "https://x.com/intent/user?user_id=$ID" "https://x.com/i/user/$ID"; do
  handle=$(
    "$CHROME_BIN" --headless=new --disable-gpu --use-gl=swiftshader --enable-unsafe-swiftshader \
      --hide-scrollbars --no-first-run --no-default-browser-check --log-level=3 --silent --disable-logging \
      --virtual-time-budget=15000 --dump-dom "$url" 2>/dev/null \
    | tr "'" '"' \
    | grep -Eo 'https://(x|twitter)\.com/[A-Za-z0-9_]{1,15}' \
    | sed -E 's#https?://(x|twitter)\.com/##' \
    | grep -Ev '^(home|tos|privacy|explore|notifications|settings|i|intent|login|signup|share|account|compose|messages|search)$' \
    | head -n1
  )
  [ -n "$handle" ] && echo "$handle (retrieved from ${url})" && break
done
