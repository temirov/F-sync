package matrix

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"sort"
	"strings"
)

//go:embed web/static/* web/templates/*
var embeddedFS embed.FS

const (
	templateBaseName       = "base"
	templateIndexFile      = "web/templates/index.tmpl"
	templateIndexName      = "index.tmpl"
	embeddedBaseCSSPath    = "web/static/base.css"
	embeddedAppJSPath      = "web/static/app.js"
	twitterUserNameBaseURL = "https://twitter.com/"
	twitterUserIDBaseURL   = "https://twitter.com/i/user/"
	accountHandlePrefix    = "@"
	displayHandleFormat    = "%s (%s%s)"
	pageTitleText          = "Twitter Relationship Matrix"
	unknownLabelText       = "Unknown"
	embedReadErrorFormat   = "embed read %s: %w"
)

func embeddedText(path string) (string, error) {
	content, err := fs.ReadFile(embeddedFS, path)
	if err != nil {
		return "", fmt.Errorf(embedReadErrorFormat, path, err)
	}
	return string(content), nil
}

// StaticAssets exposes the embedded static asset filesystem.
func StaticAssets() (fs.FS, error) {
	return fs.Sub(embeddedFS, "web/static")
}

func parseTemplates(fileSystem fs.FS, files ...string) (*template.Template, error) {
	templateWithFuncs := template.New(templateBaseName).Funcs(template.FuncMap{
		"profileURL": func(record AccountRecord) string {
			return newAccountPresentation(record).ProfileURL()
		},
		"label": func(record AccountRecord) string {
			display := strings.TrimSpace(record.DisplayName)
			handle := strings.TrimSpace(record.UserName)
			switch {
			case display != "" && handle != "":
				return fmt.Sprintf(displayHandleFormat, display, accountHandlePrefix, handle)
			case display != "":
				return display
			case handle != "":
				return accountHandlePrefix + handle
			case record.AccountID != "":
				return record.AccountID
			default:
				return unknownLabelText
			}
		},
		"has": func(flags map[string]bool, accountID string) bool { return flags[accountID] },
	})
	parsedTemplate, err := templateWithFuncs.ParseFS(fileSystem, files...)
	if err != nil {
		return nil, err
	}
	return parsedTemplate, nil
}

func mapKeys(flags map[string]bool) []string {
	keys := make([]string, 0, len(flags))
	for key := range flags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ownerPretty(identity OwnerIdentity) string {
	display := strings.TrimSpace(identity.DisplayName)
	handle := strings.TrimSpace(identity.UserName)
	switch {
	case display != "" && handle != "":
		return fmt.Sprintf(displayHandleFormat, display, accountHandlePrefix, handle)
	case display != "":
		return display
	case handle != "":
		return accountHandlePrefix + handle
	case identity.AccountID != "":
		return identity.AccountID
	default:
		return unknownLabelText
	}
}
