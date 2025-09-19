package tests

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/handles"
)

const (
	integrationFlagName                                   = "twitter_integration"
	integrationFlagDescription                            = "enable live Twitter intent resolution integration test"
	integrationFlagDisabledMessage                        = "twitter integration test skipped because the flag is disabled"
	integrationChromeUnavailableMessage                   = "twitter integration test skipped because no Chrome binary is available"
	integrationSkipMessageFormat                          = "%s: %v"
	integrationChromeEmptyPathMessage                     = "resolved empty chrome binary path"
	integrationCommandTimeoutSeconds                      = 120
	integrationCommandGoBinary                            = "go"
	integrationCommandRunArg                              = "run"
	integrationCommandModulePath                          = "./cmd/xresolve"
	integrationCommandInputFlag                           = "--in"
	integrationCommandOutputFlag                          = "--out"
	integrationCommandConcurrencyFlag                     = "--concurrency"
	integrationCommandSleepMillisFlag                     = "--sleep-ms"
	integrationCommandConcurrencyValue                    = "1"
	integrationCommandSleepMillisValue                    = "0"
	integrationInputFileName                              = "ids.txt"
	integrationOutputFileName                             = "results.csv"
	integrationInputLineSeparator                         = "\n"
	integrationInputFilePermission            os.FileMode = 0o600
	integrationRepositoryRootRelativePath                 = ".."
	integrationCommandRunErrorFormat                      = "execute xresolve CLI: %v\n%s"
	integrationRepositoryRootErrorFormat                  = "resolve repository root: %v"
	integrationInputWriteErrorFormat                      = "write integration input file: %v"
	integrationCSVParseErrorFormat                        = "parse CLI CSV output: %v"
	integrationCSVOpenErrorFormat                         = "open xresolve CSV output %s: %v"
	integrationCSVReadErrorFormat                         = "read xresolve CSV output %s: %v"
	integrationCSVEmptyMessage                            = "xresolve CSV output did not contain any rows"
	integrationCSVMissingColumnFormat                     = "xresolve CSV output missing required column %s"
	integrationCSVRowLengthFormat                         = "xresolve CSV row had insufficient columns: %v"
	integrationCSVRecordMissingFormat                     = "xresolve CSV output missing record for %s"
	integrationCSVUnexpectedHandleFormat                  = "expected handle %s, got %s"
	integrationCSVUnexpectedDisplayNameFormat             = "expected display name %s, got %s"
	integrationEnvAssignmentFormat                        = "%s=%s"
	integrationBackendEnvVariableName                     = "XRESOLVE_BACKEND"
	integrationBackendEnvVariableValue                    = "cresolve"
	integrationCSVColumnID                                = "id"
	integrationCSVColumnHandle                            = "handle"
	integrationCSVColumnDisplayName                       = "display_name"
	integrationTestCaseNameElon                           = "resolve elon musk via xresolve CLI"
	integrationAccountIDElon                              = "44196397"
	integrationExpectedHandleElon                         = "elonmusk"
	integrationExpectedDisplayNameElon                    = "Elon Musk"
)

var integrationRunFlag = flag.Bool(integrationFlagName, false, integrationFlagDescription)

// xresolveIntegrationTestCase defines a CLI integration scenario for xresolve.
type xresolveIntegrationTestCase struct {
	scenarioName    string
	numericIDs      []string
	expectedRecords map[string]expectedCSVRecord
}

// expectedCSVRecord captures the expected handle metadata for a numeric ID.
type expectedCSVRecord struct {
	handle      string
	displayName string
}

// csvResolutionRecord represents a parsed CSV row from the xresolve CLI output.
type csvResolutionRecord struct {
	handle      string
	displayName string
}

// csvResolutionResults stores CSV resolution records keyed by numeric ID.
type csvResolutionResults struct {
	records map[string]csvResolutionRecord
}

// TestXResolveCLIIntegration verifies that the xresolve CLI resolves handles using the cresolve backend.
func TestXResolveCLIIntegration(t *testing.T) {
	if !*integrationRunFlag {
		t.Skip(integrationFlagDisabledMessage)
	}

	chromeBinaryPath, chromeErr := resolveChromeBinaryPathForIntegration()
	if chromeErr != nil {
		t.Skipf(integrationSkipMessageFormat, integrationChromeUnavailableMessage, chromeErr)
	}

	repositoryRootPath, repositoryErr := filepath.Abs(integrationRepositoryRootRelativePath)
	if repositoryErr != nil {
		t.Fatalf(integrationRepositoryRootErrorFormat, repositoryErr)
	}

	integrationScenarios := []xresolveIntegrationTestCase{
		{
			scenarioName: integrationTestCaseNameElon,
			numericIDs:   []string{integrationAccountIDElon},
			expectedRecords: map[string]expectedCSVRecord{
				integrationAccountIDElon: {
					handle:      integrationExpectedHandleElon,
					displayName: integrationExpectedDisplayNameElon,
				},
			},
		},
	}

	for _, integrationScenario := range integrationScenarios {
		integrationScenario := integrationScenario
		t.Run(integrationScenario.scenarioName, func(t *testing.T) {
			commandContext, cancel := context.WithTimeout(context.Background(), time.Duration(integrationCommandTimeoutSeconds)*time.Second)
			defer cancel()

			temporaryDirectory := t.TempDir()
			inputFilePath := filepath.Join(temporaryDirectory, integrationInputFileName)
			outputFilePath := filepath.Join(temporaryDirectory, integrationOutputFileName)

			inputContent := strings.Join(integrationScenario.numericIDs, integrationInputLineSeparator) + integrationInputLineSeparator
			if writeErr := os.WriteFile(inputFilePath, []byte(inputContent), integrationInputFilePermission); writeErr != nil {
				t.Fatalf(integrationInputWriteErrorFormat, writeErr)
			}

			commandArguments := []string{
				integrationCommandRunArg,
				integrationCommandModulePath,
				integrationCommandInputFlag,
				inputFilePath,
				integrationCommandOutputFlag,
				outputFilePath,
				integrationCommandConcurrencyFlag,
				integrationCommandConcurrencyValue,
				integrationCommandSleepMillisFlag,
				integrationCommandSleepMillisValue,
			}

			command := exec.CommandContext(commandContext, integrationCommandGoBinary, commandArguments...)
			command.Dir = repositoryRootPath

			environmentVariables := append([]string{}, os.Environ()...)
			environmentVariables = append(environmentVariables, fmt.Sprintf(integrationEnvAssignmentFormat, handles.ChromeBinaryEnvironmentVariable, chromeBinaryPath))
			environmentVariables = append(environmentVariables, fmt.Sprintf(integrationEnvAssignmentFormat, integrationBackendEnvVariableName, integrationBackendEnvVariableValue))
			command.Env = environmentVariables

			var commandOutput bytes.Buffer
			command.Stdout = &commandOutput
			command.Stderr = &commandOutput

			if runErr := command.Run(); runErr != nil {
				t.Fatalf(integrationCommandRunErrorFormat, runErr, commandOutput.String())
			}

			results, parseErr := loadCSVResults(outputFilePath)
			if parseErr != nil {
				t.Fatalf(integrationCSVParseErrorFormat, parseErr)
			}

			results.assertMatches(t, integrationScenario.expectedRecords)
		})
	}
}

func resolveChromeBinaryPathForIntegration() (string, error) {
	resolvedPath := handles.ResolveChromeBinaryPath(handles.Config{})
	trimmedPath := strings.TrimSpace(resolvedPath)
	if trimmedPath == "" {
		return "", errors.New(integrationChromeEmptyPathMessage)
	}
	if strings.ContainsRune(trimmedPath, os.PathSeparator) {
		if _, statErr := os.Stat(trimmedPath); statErr != nil {
			return "", statErr
		}
		return trimmedPath, nil
	}
	lookedPath, lookErr := exec.LookPath(trimmedPath)
	if lookErr != nil {
		return "", lookErr
	}
	return lookedPath, nil
}

func loadCSVResults(csvPath string) (csvResolutionResults, error) {
	file, openErr := os.Open(csvPath)
	if openErr != nil {
		return csvResolutionResults{}, fmt.Errorf(integrationCSVOpenErrorFormat, csvPath, openErr)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	records, readErr := reader.ReadAll()
	if readErr != nil {
		return csvResolutionResults{}, fmt.Errorf(integrationCSVReadErrorFormat, csvPath, readErr)
	}
	if len(records) == 0 {
		return csvResolutionResults{}, errors.New(integrationCSVEmptyMessage)
	}

	header := records[0]
	columnIndices := make(map[string]int, len(header))
	for index, columnName := range header {
		columnIndices[columnName] = index
	}

	requiredColumns := []string{integrationCSVColumnID, integrationCSVColumnHandle, integrationCSVColumnDisplayName}
	for _, columnName := range requiredColumns {
		if _, exists := columnIndices[columnName]; !exists {
			return csvResolutionResults{}, fmt.Errorf(integrationCSVMissingColumnFormat, columnName)
		}
	}

	results := csvResolutionResults{records: make(map[string]csvResolutionRecord, len(records)-1)}
	idIndex := columnIndices[integrationCSVColumnID]
	handleIndex := columnIndices[integrationCSVColumnHandle]
	displayNameIndex := columnIndices[integrationCSVColumnDisplayName]
	for _, row := range records[1:] {
		if len(row) <= idIndex || len(row) <= handleIndex || len(row) <= displayNameIndex {
			return csvResolutionResults{}, fmt.Errorf(integrationCSVRowLengthFormat, row)
		}
		numericID := strings.TrimSpace(row[idIndex])
		handleValue := strings.TrimSpace(row[handleIndex])
		displayNameValue := strings.TrimSpace(row[displayNameIndex])
		results.records[numericID] = csvResolutionRecord{handle: handleValue, displayName: displayNameValue}
	}
	return results, nil
}

func (results csvResolutionResults) assertMatches(t *testing.T, expectations map[string]expectedCSVRecord) {
	t.Helper()
	for numericID, expectation := range expectations {
		record, exists := results.records[numericID]
		if !exists {
			t.Fatalf(integrationCSVRecordMissingFormat, numericID)
		}
		if record.handle != expectation.handle {
			t.Fatalf(integrationCSVUnexpectedHandleFormat, expectation.handle, record.handle)
		}
		if record.displayName != expectation.displayName {
			t.Fatalf(integrationCSVUnexpectedDisplayNameFormat, expectation.displayName, record.displayName)
		}
	}
}
