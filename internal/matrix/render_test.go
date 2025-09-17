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
