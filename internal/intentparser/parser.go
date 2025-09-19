package intentparser

import (
	"errors"
	"regexp"
	"strings"
)

const (
	profileURLPattern           = `https://(?:x|twitter)\.com/([A-Za-z0-9_]{1,15})`
	htmlSingleQuoteCharacter    = "'"
	htmlDoubleQuoteCharacter    = `"`
	titleStartTag               = "<title>"
	titleEndTag                 = "</title>"
	whitespaceCharacters        = " \t\r\n"
	errMessageMissingHandle     = "twitter intent page did not contain a handle"
	reservedHandlePathAnalytics = "i"
	reservedHandlePathIntent    = "intent"
	reservedHandlePathHome      = "home"
	reservedHandlePathTerms     = "tos"
	reservedHandlePathPrivacy   = "privacy"
	reservedHandlePathExplore   = "explore"
	reservedHandlePathNotices   = "notifications"
	reservedHandlePathSettings  = "settings"
	reservedHandlePathLogin     = "login"
	reservedHandlePathSignup    = "signup"
	reservedHandlePathShare     = "share"
	reservedHandlePathAccount   = "account"
	reservedHandlePathCompose   = "compose"
	reservedHandlePathMessages  = "messages"
	reservedHandlePathSearch    = "search"
	displayNameSuffixSlashX     = " / X"
	displayNameSuffixOnX        = " on X"
	handleTokenPrefix           = "(@"
	handleTokenSuffix           = ")"
)

var (
	// ErrMissingHandle indicates that an intent page did not expose a handle.
	ErrMissingHandle = errors.New(errMessageMissingHandle)

	profileURLRegex = regexp.MustCompile(profileURLPattern)

	reservedHandleNames = map[string]struct{}{
		reservedHandlePathAnalytics: {},
		reservedHandlePathIntent:    {},
		reservedHandlePathHome:      {},
		reservedHandlePathTerms:     {},
		reservedHandlePathPrivacy:   {},
		reservedHandlePathExplore:   {},
		reservedHandlePathNotices:   {},
		reservedHandlePathSettings:  {},
		reservedHandlePathLogin:     {},
		reservedHandlePathSignup:    {},
		reservedHandlePathShare:     {},
		reservedHandlePathAccount:   {},
		reservedHandlePathCompose:   {},
		reservedHandlePathMessages:  {},
		reservedHandlePathSearch:    {},
	}
)

// IntentHTMLParser extracts handle and display name data from Twitter intent HTML.
type IntentHTMLParser struct {
	htmlContent string
}

// NewIntentHTMLParser constructs a parser for the provided HTML content.
func NewIntentHTMLParser(htmlContent string) IntentHTMLParser {
	normalizedHTML := strings.ReplaceAll(htmlContent, htmlSingleQuoteCharacter, htmlDoubleQuoteCharacter)
	return IntentHTMLParser{htmlContent: normalizedHTML}
}

// ExtractHandle returns the first non-reserved handle found in the HTML.
func (parser IntentHTMLParser) ExtractHandle() (string, error) {
	matches := profileURLRegex.FindAllStringSubmatch(parser.htmlContent, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		candidate := strings.TrimSpace(match[1])
		if candidate == "" {
			continue
		}
		if parser.isReservedHandle(candidate) {
			continue
		}
		return candidate, nil
	}
	return "", ErrMissingHandle
}

// ExtractDisplayName derives the display name from the HTML <title> tag.
func (parser IntentHTMLParser) ExtractDisplayName(handle string) string {
	titleContent := parser.titleTagContent()
	if titleContent == "" {
		return ""
	}
	cleanedTitle := trimDisplayNameSuffixes(titleContent)
	if handle != "" {
		handleToken := handleTokenPrefix + handle + handleTokenSuffix
		cleanedTitle = strings.ReplaceAll(cleanedTitle, handleToken, "")
		cleanedTitle = trimDisplayNameSuffixes(cleanedTitle)
	}
	return strings.Trim(cleanedTitle, whitespaceCharacters)
}

func (parser IntentHTMLParser) isReservedHandle(handle string) bool {
	_, reserved := reservedHandleNames[strings.ToLower(handle)]
	return reserved
}

func (parser IntentHTMLParser) titleTagContent() string {
	startIndex := strings.Index(parser.htmlContent, titleStartTag)
	if startIndex == -1 {
		return ""
	}
	startIndex += len(titleStartTag)
	endIndex := strings.Index(parser.htmlContent[startIndex:], titleEndTag)
	if endIndex == -1 {
		return ""
	}
	endIndex += startIndex
	return strings.Trim(parser.htmlContent[startIndex:endIndex], whitespaceCharacters)
}

func trimDisplayNameSuffixes(titleContent string) string {
	trimmed := strings.Trim(titleContent, whitespaceCharacters)
	if strings.HasSuffix(trimmed, displayNameSuffixSlashX) {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, displayNameSuffixSlashX))
	}
	if strings.HasSuffix(trimmed, displayNameSuffixOnX) {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, displayNameSuffixOnX))
	}
	return trimmed
}
