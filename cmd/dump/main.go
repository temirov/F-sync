package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

const (
	flagZipAName                = "zip-a"
	flagZipADescription         = "Path to first Twitter data zip"
	flagZipBName                = "zip-b"
	flagZipBDescription         = "Path to second Twitter data zip"
	flagOutName                 = "out"
	flagOutDescription          = "Output HTML file path"
	flagResolveHandlesName      = "resolve-handles"
	flagResolveHandlesDesc      = "Resolve missing handles over the network"
	defaultOutputFileName       = "twitter_relationship_matrix.html"
	missingZipErrorMessage      = "error: both --zip-a and --zip-b are required"
	handleResolutionErrorFormat = "warning: handle lookup for %s failed: %v\n"
	renderErrorFormat           = "render: %v"
	loadErrorFormat             = "read %s: %v"
	createFileErrorFormat       = "create %s: %v"
	writeFileErrorFormat        = "write %s: %v"
	handlesResolverErrorFormat  = "handles resolver: %v"
)

func main() {
	var zipPathA string
	var zipPathB string
	var outputPath string
	var resolveHandles bool

	flag.StringVar(&zipPathA, flagZipAName, "", flagZipADescription)
	flag.StringVar(&zipPathB, flagZipBName, "", flagZipBDescription)
	flag.StringVar(&outputPath, flagOutName, defaultOutputFileName, flagOutDescription)
	flag.BoolVar(&resolveHandles, flagResolveHandlesName, false, flagResolveHandlesDesc)
	flag.Parse()

	if zipPathA == "" || zipPathB == "" {
		fmt.Fprintln(os.Stderr, missingZipErrorMessage)
		os.Exit(2)
	}

	accountSetsA, ownerA, err := matrix.ReadTwitterZip(zipPathA)
	if err != nil {
		dief(loadErrorFormat, zipPathA, err)
	}
	accountSetsB, ownerB, err := matrix.ReadTwitterZip(zipPathB)
	if err != nil {
		dief(loadErrorFormat, zipPathB, err)
	}

	if resolveHandles {
		resolver, err := handles.NewResolver(handles.Config{})
		if err != nil {
			dief(handlesResolverErrorFormat, err)
		}
		resolutionErrors := matrix.MaybeResolveHandles(context.Background(), resolver, true, &accountSetsA, &accountSetsB)
		for accountID, resolutionErr := range resolutionErrors {
			fmt.Fprintf(os.Stderr, handleResolutionErrorFormat, accountID, resolutionErr)
		}
	}

	comparison := matrix.BuildComparison(accountSetsA, accountSetsB, ownerA, ownerB)

	pageHTML, err := matrix.RenderComparisonPage(matrix.ComparisonPageData{Comparison: &comparison})
	if err != nil {
		dief(renderErrorFormat, err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		dief(createFileErrorFormat, outputPath, err)
	}
	defer file.Close()

	if _, err := file.WriteString(pageHTML); err != nil {
		dief(writeFileErrorFormat, outputPath, err)
	}

	fmt.Println("Wrote", outputPath)
}

func dief(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
