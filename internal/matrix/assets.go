package matrix

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"sort"
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
			return resolveIdentityLabel(record.DisplayName, record.UserName)
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
	return resolveIdentityLabel(identity.DisplayName, identity.UserName)
}
