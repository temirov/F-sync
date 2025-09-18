package intentparser_test

import (
	"testing"

	"github.com/f-sync/fsync/internal/intentparser"
)

const (
	sampleIntentHTML          = `<html><head><title>Example Name (@example) / X</title></head><body><a href="https://x.com/example">profile</a></body></html>`
	reservedIntentHTML        = `<html><head><title>Reserved Path (@example) / X</title></head><body><a href="https://x.com/intent">intent</a><a href="https://x.com/realhandle">profile</a></body></html>`
	missingHandleIntentHTML   = `<html><head><title>Unknown / X</title></head><body></body></html>`
	onXTitleIntentHTML        = `<html><head><title>Example On X (@example) on X</title></head><body><a href="https://x.com/example">profile</a></body></html>`
	handleFreeTitleIntentHTML = `<html><head><title>Plain Example / X</title></head><body><a href="https://x.com/example">profile</a></body></html>`
)

func TestIntentHTMLParserExtractHandle(t *testing.T) {
	testCases := []struct {
		name           string
		htmlContent    string
		expectedHandle string
		expectError    bool
	}{
		{
			name:           "extracts handle from profile url",
			htmlContent:    sampleIntentHTML,
			expectedHandle: "example",
		},
		{
			name:           "skips reserved paths",
			htmlContent:    reservedIntentHTML,
			expectedHandle: "realhandle",
		},
		{
			name:        "missing handle returns error",
			htmlContent: missingHandleIntentHTML,
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			parser := intentparser.NewIntentHTMLParser(testCase.htmlContent)
			handle, err := parser.ExtractHandle()
			if testCase.expectError {
				if err == nil {
					t.Fatalf("expected error extracting handle")
				}
				if err != intentparser.ErrMissingHandle {
					t.Fatalf("unexpected error type: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handle != testCase.expectedHandle {
				t.Fatalf("expected handle %s, got %s", testCase.expectedHandle, handle)
			}
		})
	}
}

func TestIntentHTMLParserExtractDisplayName(t *testing.T) {
	testCases := []struct {
		name            string
		htmlContent     string
		handle          string
		expectedDisplay string
	}{
		{
			name:            "trims slash suffix",
			htmlContent:     sampleIntentHTML,
			handle:          "example",
			expectedDisplay: "Example Name",
		},
		{
			name:            "removes on x suffix",
			htmlContent:     onXTitleIntentHTML,
			handle:          "example",
			expectedDisplay: "Example On X",
		},
		{
			name:            "handles empty handle argument",
			htmlContent:     handleFreeTitleIntentHTML,
			handle:          "",
			expectedDisplay: "Plain Example",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			parser := intentparser.NewIntentHTMLParser(testCase.htmlContent)
			displayName := parser.ExtractDisplayName(testCase.handle)
			if displayName != testCase.expectedDisplay {
				t.Fatalf("expected display name %s, got %s", testCase.expectedDisplay, displayName)
			}
		})
	}
}
