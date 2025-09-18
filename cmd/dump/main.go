package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

const (
	flagZipAName                = "zip-a"
	flagZipADescription         = "Path to first Twitter data zip"
	flagZipBName                = "zip-b"
	flagZipBDescription         = "Path to second Twitter data zip"
	flagOutName                 = "out"
	flagOutDescription          = "Output HTML file path"
	defaultOutputFileName       = "twitter_relationship_matrix.html"
	missingZipErrorMessage      = "error: both --zip-a and --zip-b are required"
	handleResolutionErrorFormat = "warning: handle lookup for %s failed: %v\n"
	renderErrorFormat           = "render: %v"
	loadErrorFormat             = "read %s: %v"
	createFileErrorFormat       = "create %s: %v"
	writeFileErrorFormat        = "write %s: %v"
	handlesResolverErrorFormat  = "handles resolver: %v"
	writeSuccessMessageFormat   = "Wrote %s"
)

func main() {
	var zipPathA string
	var zipPathB string
	var outputPath string

	flag.StringVar(&zipPathA, flagZipAName, "", flagZipADescription)
	flag.StringVar(&zipPathB, flagZipBName, "", flagZipBDescription)
	flag.StringVar(&outputPath, flagOutName, defaultOutputFileName, flagOutDescription)
	flag.Parse()

	if zipPathA == "" || zipPathB == "" {
		fmt.Fprintln(os.Stderr, missingZipErrorMessage)
		os.Exit(2)
	}

	application := NewDumpApplication()
	configuration := DumpConfiguration{ZipPathA: zipPathA, ZipPathB: zipPathB, OutputPath: outputPath}
	if err := application.Run(context.Background(), configuration); err != nil {
		dief("%v", err)
	}
}

func dief(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
