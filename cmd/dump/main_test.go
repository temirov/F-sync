package main_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	dump "github.com/f-sync/fsync/cmd/dump"
	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

const (
	testZipPathA                  = "zip-a-path"
	testZipPathB                  = "zip-b-path"
	testOutputPath                = "output.html"
	testAccountID                 = "555123"
	testResolvedUserName          = "resolved_user"
	testResolvedDisplayName       = "Resolved User"
	testRenderedHTMLDocument      = "<html>ok</html>"
	handleResolutionWarningFormat = "warning: handle lookup for %s failed: %s\n"
	testResolutionErrorMessage    = "fetch failed"
)

type resolverStub struct {
	callCount   int
	expectedIDs []string
	resultsByID map[string]handles.Result
}

func (resolver *resolverStub) ResolveMany(_ context.Context, accountIDs []string) map[string]handles.Result {
	resolver.callCount++
	resolver.expectedIDs = append(resolver.expectedIDs, accountIDs...)
	return resolver.resultsByID
}

type comparisonRendererStub struct {
	callCount int
	lastData  matrix.ComparisonPageData
}

func (renderer *comparisonRendererStub) Render(pageData matrix.ComparisonPageData) (string, error) {
	renderer.callCount++
	renderer.lastData = pageData
	return testRenderedHTMLDocument, nil
}

type outputWriterStub struct {
	calls []outputWriteCall
}

type outputWriteCall struct {
	path     string
	contents string
}

func (writer *outputWriterStub) Write(path string, contents string) error {
	writer.calls = append(writer.calls, outputWriteCall{path: path, contents: contents})
	return nil
}

func TestDumpApplicationResolvesHandlesByDefault(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		resolverResults     map[string]handles.Result
		expectedWarning     string
		expectedUserName    string
		expectedDisplayName string
	}{
		{
			name: "successful resolution",
			resolverResults: map[string]handles.Result{
				testAccountID: {
					Record: handles.AccountRecord{AccountID: testAccountID, UserName: testResolvedUserName, DisplayName: testResolvedDisplayName},
				},
			},
			expectedUserName:    testResolvedUserName,
			expectedDisplayName: testResolvedDisplayName,
		},
		{
			name: "resolution failure emits warning",
			resolverResults: map[string]handles.Result{
				testAccountID: {
					Record: handles.AccountRecord{AccountID: testAccountID},
					Err:    errors.New(testResolutionErrorMessage),
				},
			},
			expectedWarning: fmt.Sprintf(handleResolutionWarningFormat, testAccountID, testResolutionErrorMessage),
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			archiveLoader := func(string) (matrix.AccountSets, matrix.OwnerIdentity, error) {
				accountRecord := matrix.AccountRecord{AccountID: testAccountID}
				accountSets := matrix.AccountSets{
					Followers: map[string]matrix.AccountRecord{testAccountID: accountRecord},
					Following: map[string]matrix.AccountRecord{testAccountID: accountRecord},
				}
				ownerIdentity := matrix.OwnerIdentity{AccountID: testAccountID}
				return accountSets, ownerIdentity, nil
			}

			resolver := &resolverStub{resultsByID: testCase.resolverResults}
			renderer := &comparisonRendererStub{}
			writer := &outputWriterStub{}
			stdoutBuffer := &bytes.Buffer{}
			stderrBuffer := &bytes.Buffer{}

			dependencies := dump.DumpDependencies{
				ReadArchive:      archiveLoader,
				BuildResolver:    func() (matrix.AccountHandleResolver, error) { return resolver, nil },
				RenderComparison: renderer.Render,
				WriteOutputFile:  writer.Write,
				Stdout:           stdoutBuffer,
				Stderr:           stderrBuffer,
			}

			application := dump.NewDumpApplicationWithDependencies(dependencies)
			configuration := dump.DumpConfiguration{ZipPathA: testZipPathA, ZipPathB: testZipPathB, OutputPath: testOutputPath}

			if runError := application.Run(context.Background(), configuration); runError != nil {
				t.Fatalf("unexpected run error: %v", runError)
			}

			if resolver.callCount != 1 {
				t.Fatalf("expected resolver to be invoked once, got %d", resolver.callCount)
			}
			if len(resolver.expectedIDs) == 0 || resolver.expectedIDs[0] != testAccountID {
				t.Fatalf("expected resolver to receive account id %s, got %v", testAccountID, resolver.expectedIDs)
			}

			if renderer.callCount != 1 {
				t.Fatalf("expected renderer to be invoked once, got %d", renderer.callCount)
			}
			if renderer.lastData.Comparison == nil {
				t.Fatalf("expected comparison data to be provided")
			}

			accountRecord := renderer.lastData.Comparison.AccountSetsA.Followers[testAccountID]
			if accountRecord.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected username enrichment: %q", accountRecord.UserName)
			}
			if accountRecord.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected display name enrichment: %q", accountRecord.DisplayName)
			}

			if len(writer.calls) != 1 {
				t.Fatalf("expected output writer to be invoked once, got %d", len(writer.calls))
			}
			if writer.calls[0].path != testOutputPath {
				t.Fatalf("unexpected output path: %s", writer.calls[0].path)
			}
			if writer.calls[0].contents != testRenderedHTMLDocument {
				t.Fatalf("unexpected rendered contents: %s", writer.calls[0].contents)
			}

			expectedStdout := fmt.Sprintf("Wrote %s\n", testOutputPath)
			if stdoutBuffer.String() != expectedStdout {
				t.Fatalf("unexpected stdout: %q", stdoutBuffer.String())
			}

			if stderrBuffer.String() != testCase.expectedWarning {
				t.Fatalf("unexpected stderr: %q", stderrBuffer.String())
			}
		})
	}
}
