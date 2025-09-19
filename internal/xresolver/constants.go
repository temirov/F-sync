// internal/xresolver/constants.go
package xresolver

import "regexp"

var (
	// Matches full profile URLs we later strip to a handle
	ProfileURLRegex = regexp.MustCompile(`https://(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}`)

	// Reserved top-level paths to exclude as "handles"
	ReservedTopLevelPaths = map[string]struct{}{
		"i": {}, "intent": {}, "home": {}, "tos": {}, "privacy": {}, "explore": {},
		"notifications": {}, "settings": {}, "login": {}, "signup": {}, "share": {},
		"account": {}, "compose": {}, "messages": {}, "search": {},
	}

	// Meta extraction (quotes should be normalized before applying)
	MetaOGTitle  = regexp.MustCompile(`property="og:title"[^>]*content="([^"]+)"`)
	MetaTitleTag = regexp.MustCompile(`<title[^>]*>([^<]+)</title>`)
)

// Default UA pool for rotation (kept short on purpose)
var DefaultUAs = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
}
