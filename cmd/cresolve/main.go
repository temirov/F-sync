package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/f-sync/fsync/internal/cresolver"
	"github.com/f-sync/fsync/internal/handles"
)

const (
	defaultChromeBinaryPath         = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	usageHeader                     = "usage:"
	usageSingleID                   = "  cresolve <numeric_id>"
	usageCSVCommand                 = "  cresolve -csv <id1> <id2> ..."
	usageJSONCommand                = "  cat ids.txt | cresolve -json"
	chromeBinaryEnvironmentVariable = "CHROME_BIN"
	csvHeaderIdentifier             = "id"
	csvHeaderHandle                 = "handle"
	csvHeaderDisplayName            = "display_name"
	errMessageReadIdentifiers       = "stdin read error"
	errMessageOutputFormatConflict  = "cannot specify both -csv and -json"
	errMessageCreateService         = "create resolver service"
	errMessageResolveIdentifiers    = "resolve account identifiers"
	errMessageWriteCSV              = "write CSV output"
)

var disabledChromeRequestDelay = -1 * time.Millisecond

type outputFormat string

const (
	outputFormatText outputFormat = "text"
	outputFormatCSV  outputFormat = "csv"
	outputFormatJSON outputFormat = "json"
)

type profileOutput struct {
	Identifier  string `json:"id"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	SourceURL   string `json:"from_url"`
	Error       string `json:"error,omitempty"`
}

type cliOptions struct {
	outputFormat      outputFormat
	chromeBinaryPath  string
	virtualTimeBudget time.Duration
	accountTimeout    time.Duration
	requestDelay      time.Duration
	requestJitter     time.Duration
	burstSize         int
	burstRest         time.Duration
	burstRestJitter   time.Duration
}

func main() {
	options, ids := parseArguments()
	if len(ids) == 0 {
		printUsage()
		os.Exit(2)
	}

	serviceConfig := cresolver.Config{
		Handles: handles.Config{
			ChromeBinaryPath:        options.chromeBinaryPath,
			ChromeUserAgent:         handles.DefaultChromeUserAgent(nil),
			ChromeVirtualTimeBudget: options.virtualTimeBudget,
			ChromeRequestDelay:      disabledChromeRequestDelay,
		},
		AccountTimeout: options.accountTimeout,
		RequestPacing: cresolver.RequestPacingConfig{
			BaseDelay:       options.requestDelay,
			Jitter:          options.requestJitter,
			BurstSize:       options.burstSize,
			BurstRest:       options.burstRest,
			BurstRestJitter: options.burstRestJitter,
		},
	}

	resolverService, serviceErr := cresolver.NewService(serviceConfig)
	if serviceErr != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", errMessageCreateService, serviceErr)
		os.Exit(1)
	}

	applicationContext, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	resolutions, resolveErr := resolverService.ResolveBatch(applicationContext, cresolver.Request{AccountIDs: ids})

	csvWriter := initializeCSVWriter(options.outputFormat)
	jsonEncoder := initializeJSONEncoder(options.outputFormat)

	for _, resolution := range resolutions {
		output := profileOutput{
			Identifier:  resolution.AccountID,
			Handle:      resolution.Record.UserName,
			DisplayName: resolution.Record.DisplayName,
			SourceURL:   resolution.IntentURL,
		}
		if resolution.Err != nil {
			output.Error = resolution.Err.Error()
		}

		switch options.outputFormat {
		case outputFormatCSV:
			if csvWriter != nil {
				_ = csvWriter.Write([]string{output.Identifier, output.Handle, output.DisplayName})
			}
		case outputFormatJSON:
			if jsonEncoder != nil {
				_ = jsonEncoder.Encode(output)
			}
		default:
			emitTextOutput(output)
		}
	}

	if csvWriter != nil {
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", errMessageWriteCSV, err)
			os.Exit(1)
		}
	}

	if resolveErr != nil {
		if !errors.Is(resolveErr, context.Canceled) {
			fmt.Fprintf(os.Stderr, "%s: %v\n", errMessageResolveIdentifiers, resolveErr)
		}
		os.Exit(1)
	}
}

func parseArguments() (cliOptions, []string) {
	csvFlag := flag.Bool("csv", false, "output CSV: id,handle,display_name")
	jsonFlag := flag.Bool("json", false, "output JSON lines")
	chromeFlag := flag.String("chrome", defaultChromeBinaryPath, "path to Chrome/Chromium binary (or set CHROME_BIN)")
	virtualTimeBudgetFlag := flag.Int("vtbudget", 15000, "Chrome virtual time budget (ms)")
	timeoutFlag := flag.Duration("timeout", 30*time.Second, "per-ID timeout")
	delayFlag := flag.Duration("delay", 500*time.Millisecond, "base delay between requests")
	jitterFlag := flag.Duration("jitter", 0, "add uniform jitter in [-jitter,+jitter] to each delay (e.g. 300ms)")
	burstSizeFlag := flag.Int("burst-size", 0, "number of requests per burst (0 disables bursting)")
	burstRestFlag := flag.Duration("burst-rest", 0, "rest duration after each burst (e.g. 5s)")
	burstRestJitterFlag := flag.Duration("burst-jitter", 0, "jitter for burst rest in [-burst-jitter,+burst-jitter]")
	flag.Parse()

	output := determineOutputFormat(*csvFlag, *jsonFlag)

	chromeBinaryPath := os.Getenv(chromeBinaryEnvironmentVariable)
	if strings.TrimSpace(chromeBinaryPath) == "" {
		chromeBinaryPath = *chromeFlag
	}

	options := cliOptions{
		outputFormat:      output,
		chromeBinaryPath:  chromeBinaryPath,
		virtualTimeBudget: time.Duration(*virtualTimeBudgetFlag) * time.Millisecond,
		accountTimeout:    *timeoutFlag,
		requestDelay:      *delayFlag,
		requestJitter:     *jitterFlag,
		burstSize:         *burstSizeFlag,
		burstRest:         *burstRestFlag,
		burstRestJitter:   *burstRestJitterFlag,
	}

	identifiers := collectIdentifiers(flag.Args())
	if len(identifiers) == 0 {
		stdinIdentifiers, err := readIdentifiersFromStdin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", errMessageReadIdentifiers, err)
			os.Exit(2)
		}
		identifiers = stdinIdentifiers
	}
	return options, identifiers
}

func determineOutputFormat(csvRequested bool, jsonRequested bool) outputFormat {
	if csvRequested && jsonRequested {
		fmt.Fprintln(os.Stderr, errMessageOutputFormatConflict)
		os.Exit(2)
	}
	if csvRequested {
		return outputFormatCSV
	}
	if jsonRequested {
		return outputFormatJSON
	}
	return outputFormatText
}

func collectIdentifiers(arguments []string) []string {
	identifiers := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		trimmed := strings.TrimSpace(argument)
		if trimmed == "" {
			continue
		}
		identifiers = append(identifiers, trimmed)
	}
	return identifiers
}

func readIdentifiersFromStdin() ([]string, error) {
	scanner := bufio.NewScanner(os.Stdin)
	identifiers := []string{}
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" {
			continue
		}
		identifiers = append(identifiers, trimmed)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return identifiers, nil
}

func initializeCSVWriter(format outputFormat) *csv.Writer {
	if format != outputFormatCSV {
		return nil
	}
	writer := csv.NewWriter(os.Stdout)
	_ = writer.Write([]string{csvHeaderIdentifier, csvHeaderHandle, csvHeaderDisplayName})
	writer.Flush()
	return writer
}

func initializeJSONEncoder(format outputFormat) *json.Encoder {
	if format != outputFormatJSON {
		return nil
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder
}

func emitTextOutput(output profileOutput) {
	if output.Handle != "" {
		fmt.Printf("%s: %s (retrieved from %s)\n", output.Identifier, output.Handle, output.SourceURL)
		if output.DisplayName != "" {
			fmt.Printf("  name: %s\n", output.DisplayName)
		}
	} else {
		fmt.Printf("%s:\n", output.Identifier)
		if output.Error != "" {
			fmt.Printf("  err:  %s\n", output.Error)
		}
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, usageHeader)
	fmt.Fprintln(os.Stderr, usageSingleID)
	fmt.Fprintln(os.Stderr, usageCSVCommand)
	fmt.Fprintln(os.Stderr, usageJSONCommand)
}
