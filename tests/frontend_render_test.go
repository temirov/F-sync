package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	frontendAppScriptRelativePath      = "../internal/matrix/web/static/app.js"
	frontendClosureSuffix              = "})();"
	frontendInstrumentedHookName       = "__fsyncRenderAccountRecord"
	frontendInstrumentedScriptFileName = "app_instrumented.js"
	frontendDriverScriptFileName       = "driver.js"
	frontendPlaceholderText            = "Unknown"
	frontendIDLinkFragment             = "/i/user/"
	frontendNodeCommand                = "node"
)

const frontendNodeDriverTemplate = `const vm = require("vm");
const fs = require("fs");

const scriptPath = process.argv[2];
const payloadJSON = process.argv[3];
const scriptContent = fs.readFileSync(scriptPath, "utf8");

function createStubElement() {
    return {
        className: "",
        textContent: "",
        innerHTML: "",
        dataset: {},
        classList: {
            add() {},
            remove() {},
            toggle() {},
        },
        appendChild() {},
        setAttribute() {},
        addEventListener() {},
        getAttribute() { return ""; },
    };
}

const sandbox = {
    console,
    document: {
        getElementById() { return null; },
        querySelectorAll() { return []; },
        createElement() { return createStubElement(); },
    },
    window: { location: { reload() {} } },
    setTimeout,
    clearTimeout,
};

vm.createContext(sandbox);
new vm.Script(scriptContent, { filename: scriptPath }).runInContext(sandbox);

const renderAccountRecordHook = sandbox.%s;
if (typeof renderAccountRecordHook !== "function") {
    throw new Error("renderAccountRecord hook missing");
}

const payload = JSON.parse(payloadJSON);
const html = renderAccountRecordHook(payload.record, payload.metaSources, payload.includeFollowAction);
process.stdout.write(html);
`

func TestFrontEndRenderAccountRecordOmitsNumericIdentifier(t *testing.T) {
	appScriptBytes, readErr := os.ReadFile(frontendAppScriptRelativePath)
	if readErr != nil {
		t.Fatalf("read app.js: %v", readErr)
	}
	scriptText := string(appScriptBytes)
	closureIndex := strings.LastIndex(scriptText, frontendClosureSuffix)
	if closureIndex == -1 {
		t.Fatalf("unable to locate closure suffix %q in app.js", frontendClosureSuffix)
	}
	instrumentedScriptText := scriptText[:closureIndex] + fmt.Sprintf("    globalThis.%s = renderAccountRecord;\n", frontendInstrumentedHookName) + frontendClosureSuffix

	temporaryDirectory := t.TempDir()
	instrumentedScriptPath := filepath.Join(temporaryDirectory, frontendInstrumentedScriptFileName)
	if writeErr := os.WriteFile(instrumentedScriptPath, []byte(instrumentedScriptText), 0o600); writeErr != nil {
		t.Fatalf("write instrumented script: %v", writeErr)
	}

	driverScriptPath := filepath.Join(temporaryDirectory, frontendDriverScriptFileName)
	driverScriptText := fmt.Sprintf(frontendNodeDriverTemplate, frontendInstrumentedHookName)
	if writeErr := os.WriteFile(driverScriptPath, []byte(driverScriptText), 0o600); writeErr != nil {
		t.Fatalf("write driver script: %v", writeErr)
	}

	testCases := []struct {
		name                string
		record              map[string]string
		includeFollowAction bool
	}{
		{
			name:                "only numeric identifier present",
			record:              map[string]string{"AccountID": "13579"},
			includeFollowAction: false,
		},
		{
			name:                "numeric identifier with follow action requested",
			record:              map[string]string{"AccountID": "24680"},
			includeFollowAction: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			payload := struct {
				Record              map[string]string `json:"record"`
				MetaSources         []map[string]any  `json:"metaSources"`
				IncludeFollowAction bool              `json:"includeFollowAction"`
			}{
				Record:              testCase.record,
				MetaSources:         []map[string]any{},
				IncludeFollowAction: testCase.includeFollowAction,
			}

			payloadBytes, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				t.Fatalf("marshal payload: %v", marshalErr)
			}

			command := exec.Command(frontendNodeCommand, driverScriptPath, instrumentedScriptPath, string(payloadBytes))
			var stderrBuffer bytes.Buffer
			command.Stderr = &stderrBuffer
			outputBytes, runErr := command.Output()
			if runErr != nil {
				t.Fatalf("execute node driver: %v\n%s", runErr, stderrBuffer.String())
			}

			htmlOutput := string(outputBytes)
			if strings.Contains(htmlOutput, testCase.record["AccountID"]) {
				t.Fatalf("expected rendered HTML to omit numeric identifier %q, got %q", testCase.record["AccountID"], htmlOutput)
			}

			if !strings.Contains(htmlOutput, frontendPlaceholderText) {
				t.Fatalf("expected rendered HTML to include placeholder %q, got %q", frontendPlaceholderText, htmlOutput)
			}

			if strings.Contains(htmlOutput, frontendIDLinkFragment) {
				t.Fatalf("expected rendered HTML to avoid ID-based profile links containing %q, got %q", frontendIDLinkFragment, htmlOutput)
			}
		})
	}
}
