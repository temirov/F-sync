package matrix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
)

// ComparisonPageData captures the state needed to render the interactive comparison page.
type ComparisonPageData struct {
	Comparison *ComparisonResult
	Uploads    []UploadSummary
	Errors     []string
}

// RenderComparisonPage assembles the HTML output using the embedded assets and templates.
func RenderComparisonPage(pageData ComparisonPageData) (string, error) {
	cssText, err := embeddedText(embeddedBaseCSSPath)
	if err != nil {
		return "", err
	}
	jsText, err := embeddedText(embeddedAppJSPath)
	if err != nil {
		return "", err
	}
	matrixJSON := ""
	if pageData.Comparison != nil {
		matrixJSON, err = buildMatrixJSON(*pageData.Comparison)
		if err != nil {
			return "", err
		}
	}
	viewModel := newComparisonPageViewModel(pageData, cssText, jsText, matrixJSON)
	tmpl, err := parseTemplates(embeddedFS, templateIndexFile)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buffer, templateIndexName, viewModel); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return buffer.String(), nil
}

type comparisonPageViewModel struct {
	Title         string
	HasComparison bool

	OwnerA string
	OwnerB string

	Counts struct {
		A struct{ Followers, Following, Friends, Leaders, Groupies, Muted, Blocked int }
		B struct{ Followers, Following, Friends, Leaders, Groupies, Muted, Blocked int }
	}

	OwnerALists ownerListViewModel
	OwnerBLists ownerListViewModel

	Uploads []uploadSummaryViewModel
	Errors  []string

	MatrixJSON template.JS
	CSS        template.CSS
	JS         template.JS
}

type uploadSummaryViewModel struct {
	SlotLabel  string
	OwnerLabel string
	FileName   string
}

type ownerListViewModel struct {
	Friends             []accountCardTemplateData
	Leaders             []accountCardTemplateData
	Groupies            []accountCardTemplateData
	BlockedAll          []accountCardTemplateData
	BlockedAndFollowing []accountCardTemplateData
	BlockedAndFollowers []accountCardTemplateData
}

type accountCardTemplateData struct {
	Presentation accountPresentation
	Muted        bool
	Blocked      bool
}

type accountPresentation struct {
	record AccountRecord
}

func newAccountPresentation(record AccountRecord) accountPresentation {
	return accountPresentation{record: record}
}

func (presentation accountPresentation) Display() string {
	return resolveIdentityLabel(presentation.record.DisplayName, presentation.record.UserName)
}

func (presentation accountPresentation) Handle() string {
	return resolveHandleLabel(presentation.record.UserName)
}

func (presentation accountPresentation) ProfileURL() string {
	trimmedHandle := strings.TrimSpace(presentation.record.UserName)
	if trimmedHandle == "" {
		return ""
	}
	return twitterUserNameBaseURL + trimmedHandle
}

type accountBadgeDecorator struct {
	mutedIDs   map[string]bool
	blockedIDs map[string]bool
}

func newAccountBadgeDecorator(mutedIDs map[string]bool, blockedIDs map[string]bool) accountBadgeDecorator {
	return accountBadgeDecorator{mutedIDs: mutedIDs, blockedIDs: blockedIDs}
}

func (decorator accountBadgeDecorator) Decorate(records []AccountRecord) []accountCardTemplateData {
	if len(records) == 0 {
		return nil
	}
	decorated := make([]accountCardTemplateData, 0, len(records))
	for _, record := range records {
		decorated = append(decorated, accountCardTemplateData{
			Presentation: newAccountPresentation(record),
			Muted:        decorator.isMuted(record.AccountID),
			Blocked:      decorator.isBlocked(record.AccountID),
		})
	}
	return decorated
}

func (decorator accountBadgeDecorator) isMuted(accountID string) bool {
	if decorator.mutedIDs == nil {
		return false
	}
	return decorator.mutedIDs[accountID]
}

func (decorator accountBadgeDecorator) isBlocked(accountID string) bool {
	if decorator.blockedIDs == nil {
		return false
	}
	return decorator.blockedIDs[accountID]
}

func newComparisonPageViewModel(pageData ComparisonPageData, cssText string, jsText string, matrixJSON string) comparisonPageViewModel {
	viewModel := comparisonPageViewModel{
		Title: pageTitleText,
		CSS:   template.CSS(cssText),
		JS:    template.JS(jsText),
	}

	if len(pageData.Errors) > 0 {
		viewModel.Errors = append(viewModel.Errors, pageData.Errors...)
	}

	if len(pageData.Uploads) > 0 {
		viewModel.Uploads = make([]uploadSummaryViewModel, 0, len(pageData.Uploads))
		for _, upload := range pageData.Uploads {
			viewModel.Uploads = append(viewModel.Uploads, uploadSummaryViewModel{
				SlotLabel:  upload.SlotLabel,
				OwnerLabel: upload.OwnerLabel,
				FileName:   upload.FileName,
			})
		}
	}

	if pageData.Comparison == nil {
		return viewModel
	}

	comparison := *pageData.Comparison
	ownerADecorator := newAccountBadgeDecorator(comparison.AccountSetsA.Muted, comparison.AccountSetsA.Blocked)
	ownerBDecorator := newAccountBadgeDecorator(comparison.AccountSetsB.Muted, comparison.AccountSetsB.Blocked)

	viewModel.HasComparison = true
	viewModel.OwnerA = ownerPretty(comparison.OwnerA)
	viewModel.OwnerB = ownerPretty(comparison.OwnerB)
	viewModel.OwnerALists = ownerListViewModel{
		Friends:             ownerADecorator.Decorate(comparison.OwnerAFriends),
		Leaders:             ownerADecorator.Decorate(comparison.OwnerALeaders),
		Groupies:            ownerADecorator.Decorate(comparison.OwnerAGroupies),
		BlockedAll:          ownerADecorator.Decorate(comparison.OwnerABlockedAll),
		BlockedAndFollowing: ownerADecorator.Decorate(comparison.OwnerABlockedAndFollowing),
		BlockedAndFollowers: ownerADecorator.Decorate(comparison.OwnerABlockedAndFollowers),
	}
	viewModel.OwnerBLists = ownerListViewModel{
		Friends:             ownerBDecorator.Decorate(comparison.OwnerBFriends),
		Leaders:             ownerBDecorator.Decorate(comparison.OwnerBLeaders),
		Groupies:            ownerBDecorator.Decorate(comparison.OwnerBGroupies),
		BlockedAll:          ownerBDecorator.Decorate(comparison.OwnerBBlockedAll),
		BlockedAndFollowing: ownerBDecorator.Decorate(comparison.OwnerBBlockedAndFollowing),
		BlockedAndFollowers: ownerBDecorator.Decorate(comparison.OwnerBBlockedAndFollowers),
	}
	viewModel.MatrixJSON = template.JS(matrixJSON)
	viewModel.Counts.A.Followers = len(comparison.OwnerAFollowersAll)
	viewModel.Counts.A.Following = len(comparison.OwnerAFollowingsAll)
	viewModel.Counts.A.Friends = len(comparison.OwnerAFriends)
	viewModel.Counts.A.Leaders = len(comparison.OwnerALeaders)
	viewModel.Counts.A.Groupies = len(comparison.OwnerAGroupies)
	viewModel.Counts.A.Muted = len(comparison.AccountSetsA.Muted)
	viewModel.Counts.A.Blocked = len(comparison.AccountSetsA.Blocked)
	viewModel.Counts.B.Followers = len(comparison.OwnerBFollowersAll)
	viewModel.Counts.B.Following = len(comparison.OwnerBFollowingsAll)
	viewModel.Counts.B.Friends = len(comparison.OwnerBFriends)
	viewModel.Counts.B.Leaders = len(comparison.OwnerBLeaders)
	viewModel.Counts.B.Groupies = len(comparison.OwnerBGroupies)
	viewModel.Counts.B.Muted = len(comparison.AccountSetsB.Muted)
	viewModel.Counts.B.Blocked = len(comparison.AccountSetsB.Blocked)
	return viewModel
}

func buildMatrixJSON(comparison ComparisonResult) (string, error) {
	matrix := struct {
		OwnerA     string `json:"ownerA"`
		OwnerB     string `json:"ownerB"`
		OwnerAData struct {
			Followers []AccountRecord `json:"followers"`
			Following []AccountRecord `json:"following"`
			Muted     []string        `json:"muted"`
			Blocked   []string        `json:"blocked"`
		} `json:"A"`
		OwnerBData struct {
			Followers []AccountRecord `json:"followers"`
			Following []AccountRecord `json:"following"`
			Muted     []string        `json:"muted"`
			Blocked   []string        `json:"blocked"`
		} `json:"B"`
	}{
		OwnerA: ownerPretty(comparison.OwnerA),
		OwnerB: ownerPretty(comparison.OwnerB),
	}
	matrix.OwnerAData.Followers = comparison.OwnerAFollowersAll
	matrix.OwnerAData.Following = comparison.OwnerAFollowingsAll
	matrix.OwnerAData.Muted = mapKeys(comparison.AccountSetsA.Muted)
	matrix.OwnerAData.Blocked = mapKeys(comparison.AccountSetsA.Blocked)
	matrix.OwnerBData.Followers = comparison.OwnerBFollowersAll
	matrix.OwnerBData.Following = comparison.OwnerBFollowingsAll
	matrix.OwnerBData.Muted = mapKeys(comparison.AccountSetsB.Muted)
	matrix.OwnerBData.Blocked = mapKeys(comparison.AccountSetsB.Blocked)

	encoded, err := json.Marshal(matrix)
	if err != nil {
		return "", fmt.Errorf("marshal matrix: %w", err)
	}
	return string(encoded), nil
}
