package matrix

import (
	"fmt"
	"strings"
)

// resolveIdentityLabel returns a display label constructed from the provided display name and handle.
// The label includes both the display name and handle when available and falls back to the handle or
// a placeholder when necessary.
func resolveIdentityLabel(displayName string, userName string) string {
	trimmedDisplayName := strings.TrimSpace(displayName)
	trimmedUserName := strings.TrimSpace(userName)
	switch {
	case trimmedDisplayName != "" && trimmedUserName != "":
		return fmt.Sprintf(displayHandleFormat, trimmedDisplayName, accountHandlePrefix, trimmedUserName)
	case trimmedDisplayName != "":
		return trimmedDisplayName
	case trimmedUserName != "":
		return accountHandlePrefix + trimmedUserName
	default:
		return unknownLabelText
	}
}

// resolveHandleLabel formats a handle with the appropriate prefix when the handle is present.
func resolveHandleLabel(userName string) string {
	trimmedUserName := strings.TrimSpace(userName)
	if trimmedUserName == "" {
		return ""
	}
	return accountHandlePrefix + trimmedUserName
}
