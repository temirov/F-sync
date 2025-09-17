package matrix_test

import (
	"strings"
	"testing"

	"github.com/f-sync/fsync/internal/matrix"
)

func TestRenderComparisonPageStructure(t *testing.T) {
	const (
		snippetAccountCard           = "class=\"account-card\""
		snippetAccountDisplay        = "<strong class=\"account-display\">Muted Blocked</strong>"
		snippetAccountHandle         = "<span class=\"account-handle\">@presented</span>"
		snippetMutedBadge            = "<span class=\"badge badge-muted\">Muted</span>"
		snippetBlockedBadge          = "<span class=\"badge badge-block\">Blocked</span>"
		snippetEmbeddedCSSClass      = ".account-card-link:hover .account-display {"
		snippetEmptyPlaceholder      = "<p class=\"muted\">None</p>"
		snippetTableOfContentsNav    = "<nav class=\"toc\" aria-label=\"Page sections\">"
		snippetTableOfContentsAnchor = "<a class=\"toc-link\" href=\"#overview\">Overview</a>"
		snippetSectionToggleControl  = "<button type=\"button\" class=\"section-toggle\" data-section-id=\"overview-content\" aria-expanded=\"true\" aria-controls=\"overview-content\">Hide</button>"
		snippetSectionContent        = "<div class=\"section-content\" id=\"overview-content\">"
	)

	decoratedRecord := matrix.AccountRecord{AccountID: "42", UserName: "presented", DisplayName: "Muted Blocked"}

	testCases := []struct {
		name             string
		comparison       matrix.ComparisonResult
		expectedSnippets []string
	}{
		{
			name: "renders account cards with badges",
			comparison: matrix.ComparisonResult{
				AccountSetsA: matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{"42": decoratedRecord},
					Following: map[string]matrix.AccountRecord{"42": decoratedRecord},
					Muted:     map[string]bool{"42": true},
					Blocked:   map[string]bool{"42": true},
				},
				AccountSetsB:        matrix.AccountSets{Muted: map[string]bool{}, Blocked: map[string]bool{}},
				OwnerA:              matrix.OwnerIdentity{AccountID: "1", UserName: "owner_a", DisplayName: "Owner A"},
				OwnerB:              matrix.OwnerIdentity{AccountID: "2", UserName: "owner_b", DisplayName: "Owner B"},
				OwnerAFriends:       []matrix.AccountRecord{decoratedRecord},
				OwnerABlockedAll:    []matrix.AccountRecord{decoratedRecord},
				OwnerAFollowersAll:  []matrix.AccountRecord{decoratedRecord},
				OwnerAFollowingsAll: []matrix.AccountRecord{decoratedRecord},
			},
			expectedSnippets: []string{
				snippetAccountCard,
				snippetAccountDisplay,
				snippetAccountHandle,
				snippetMutedBadge,
				snippetBlockedBadge,
				snippetEmbeddedCSSClass,
				snippetTableOfContentsNav,
				snippetTableOfContentsAnchor,
				snippetSectionToggleControl,
				snippetSectionContent,
			},
		},
		{
			name: "renders placeholder for empty lists",
			comparison: matrix.ComparisonResult{
				AccountSetsA: matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{},
					Following: map[string]matrix.AccountRecord{},
					Muted:     map[string]bool{},
					Blocked:   map[string]bool{},
				},
				AccountSetsB: matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{},
					Following: map[string]matrix.AccountRecord{},
					Muted:     map[string]bool{},
					Blocked:   map[string]bool{},
				},
			},
			expectedSnippets: []string{
				snippetEmptyPlaceholder,
				snippetTableOfContentsNav,
				snippetTableOfContentsAnchor,
				snippetSectionToggleControl,
				snippetSectionContent,
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			html, err := matrix.RenderComparisonPage(testCase.comparison)
			if err != nil {
				t.Fatalf("RenderComparisonPage: %v", err)
			}
			for _, snippet := range testCase.expectedSnippets {
				if !strings.Contains(html, snippet) {
					t.Fatalf("expected HTML to contain %q", snippet)
				}
			}
		})
	}
}
