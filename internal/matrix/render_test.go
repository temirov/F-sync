package matrix_test

import (
	"strings"
	"testing"

	"github.com/f-sync/fsync/internal/matrix"
)

func TestRenderComparisonPageRendersUploadInterface(t *testing.T) {
	pageData := matrix.ComparisonPageData{
		Uploads: []matrix.UploadSummary{
			{SlotLabel: "Archive A", OwnerLabel: "Owner Alpha", FileName: "alpha.zip"},
			{SlotLabel: "Archive B", OwnerLabel: "Owner Beta", FileName: "beta.zip"},
		},
	}

	html, err := matrix.RenderComparisonPage(pageData)
	if err != nil {
		t.Fatalf("RenderComparisonPage returned error: %v", err)
	}

	expectedSnippets := []string{
		"id=\"archiveDropzone\"",
		"Upload two archives and press <strong>Compare</strong>",
		"Owner Alpha",
		"data-has-comparison=\"false\"",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("expected HTML to contain %q", snippet)
		}
	}
}

func TestRenderComparisonPageWithComparisonData(t *testing.T) {
	decoratedRecord := matrix.AccountRecord{AccountID: "42", UserName: "presented", DisplayName: "Muted Blocked"}
	comparison := matrix.ComparisonResult{
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
	}

	pageData := matrix.ComparisonPageData{
		Comparison: &comparison,
		Uploads:    []matrix.UploadSummary{{SlotLabel: "Archive A", OwnerLabel: "Owner A", FileName: "a.zip"}},
	}

	html, err := matrix.RenderComparisonPage(pageData)
	if err != nil {
		t.Fatalf("RenderComparisonPage returned error: %v", err)
	}

	expectedSnippets := []string{
		"nav class=\"nav nav-pills",
		"Owner A (@owner_a) — Relationship Matrix",
		"badge text-bg-danger\">Blocked</span>",
		"data-has-comparison=\"true\"",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("expected HTML to contain %q", snippet)
		}
	}
}

func TestRenderComparisonPageOmitsNumericIdentifiersWhenMetadataMissing(t *testing.T) {
	const (
		placeholderText      = "Unknown"
		ownerAccountIDAlpha  = "9001"
		ownerAccountIDBeta   = "9002"
		followerAccountID    = "987654321"
		idLinkPrefixFragment = "https://twitter.com/i/user/"
	)

	testCases := []struct {
		name                string
		ownerA              matrix.OwnerIdentity
		ownerB              matrix.OwnerIdentity
		followerRecord      matrix.AccountRecord
		expectedPlaceholder string
		forbiddenIdentifier string
		forbiddenOwnerAID   string
		forbiddenOwnerBID   string
	}{
		{
			name:                "only account identifiers provided",
			ownerA:              matrix.OwnerIdentity{AccountID: ownerAccountIDAlpha},
			ownerB:              matrix.OwnerIdentity{AccountID: ownerAccountIDBeta},
			followerRecord:      matrix.AccountRecord{AccountID: followerAccountID},
			expectedPlaceholder: placeholderText,
			forbiddenIdentifier: followerAccountID,
			forbiddenOwnerAID:   ownerAccountIDAlpha,
			forbiddenOwnerBID:   ownerAccountIDBeta,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			comparison := matrix.ComparisonResult{
				AccountSetsA: matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{testCase.followerRecord.AccountID: testCase.followerRecord},
					Following: map[string]matrix.AccountRecord{testCase.followerRecord.AccountID: testCase.followerRecord},
					Muted:     map[string]bool{},
					Blocked:   map[string]bool{},
				},
				AccountSetsB: matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{},
					Following: map[string]matrix.AccountRecord{},
					Muted:     map[string]bool{},
					Blocked:   map[string]bool{},
				},
				OwnerA:             testCase.ownerA,
				OwnerB:             testCase.ownerB,
				OwnerAFriends:      []matrix.AccountRecord{testCase.followerRecord},
				OwnerAFollowersAll: []matrix.AccountRecord{testCase.followerRecord},
			}

			pageData := matrix.ComparisonPageData{Comparison: &comparison}
			html, err := matrix.RenderComparisonPage(pageData)
			if err != nil {
				t.Fatalf("RenderComparisonPage returned error: %v", err)
			}

			if !strings.Contains(html, testCase.expectedPlaceholder) {
				t.Fatalf("expected HTML to contain placeholder %q", testCase.expectedPlaceholder)
			}

			if strings.Contains(html, ">"+testCase.forbiddenIdentifier+"<") {
				t.Fatalf("expected account identifier %q to be hidden in rendered HTML", testCase.forbiddenIdentifier)
			}

			if strings.Contains(html, ">"+testCase.forbiddenOwnerAID+"<") {
				t.Fatalf("expected owner A identifier %q to be hidden in rendered HTML", testCase.forbiddenOwnerAID)
			}

			if strings.Contains(html, ">"+testCase.forbiddenOwnerBID+"<") {
				t.Fatalf("expected owner B identifier %q to be hidden in rendered HTML", testCase.forbiddenOwnerBID)
			}

			if strings.Contains(html, idLinkPrefixFragment) {
				t.Fatalf("expected HTML to avoid ID-based profile links containing %q", idLinkPrefixFragment)
			}
		})
	}
}
