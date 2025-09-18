package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

type DumpConfiguration struct {
	ZipPathA   string
	ZipPathB   string
	OutputPath string
}

type DumpDependencies struct {
	ReadArchive      func(string) (matrix.AccountSets, matrix.OwnerIdentity, error)
	BuildResolver    func() (matrix.AccountHandleResolver, error)
	RenderComparison func(matrix.ComparisonPageData) (string, error)
	WriteOutputFile  func(string, string) error
	Stdout           io.Writer
	Stderr           io.Writer
}

type DumpApplication struct {
	dependencies DumpDependencies
}

func NewDumpApplication() DumpApplication {
	return NewDumpApplicationWithDependencies(newDefaultDumpDependencies())
}

func NewDumpApplicationWithDependencies(dependencies DumpDependencies) DumpApplication {
	defaultDependencies := newDefaultDumpDependencies()

	if dependencies.ReadArchive == nil {
		dependencies.ReadArchive = defaultDependencies.ReadArchive
	}
	if dependencies.BuildResolver == nil {
		dependencies.BuildResolver = defaultDependencies.BuildResolver
	}
	if dependencies.RenderComparison == nil {
		dependencies.RenderComparison = defaultDependencies.RenderComparison
	}
	if dependencies.WriteOutputFile == nil {
		dependencies.WriteOutputFile = defaultDependencies.WriteOutputFile
	}
	if dependencies.Stdout == nil {
		dependencies.Stdout = defaultDependencies.Stdout
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = defaultDependencies.Stderr
	}

	return DumpApplication{dependencies: dependencies}
}

func (application DumpApplication) Run(executionContext context.Context, configuration DumpConfiguration) error {
	accountSetsOwnerA, ownerIdentityA, readArchiveError := application.dependencies.ReadArchive(configuration.ZipPathA)
	if readArchiveError != nil {
		return fmt.Errorf(loadErrorFormat, configuration.ZipPathA, readArchiveError)
	}
	accountSetsOwnerB, ownerIdentityB, readArchiveError := application.dependencies.ReadArchive(configuration.ZipPathB)
	if readArchiveError != nil {
		return fmt.Errorf(loadErrorFormat, configuration.ZipPathB, readArchiveError)
	}

	handleResolver, resolverError := application.dependencies.BuildResolver()
	if resolverError != nil {
		return fmt.Errorf(handlesResolverErrorFormat, resolverError)
	}

	resolutionErrors := matrix.MaybeResolveHandles(executionContext, handleResolver, true, &accountSetsOwnerA, &accountSetsOwnerB)
	for accountID, resolutionError := range resolutionErrors {
		fmt.Fprintf(application.dependencies.Stderr, handleResolutionErrorFormat, accountID, resolutionError)
	}

	comparison := matrix.BuildComparison(accountSetsOwnerA, accountSetsOwnerB, ownerIdentityA, ownerIdentityB)

	pageHTML, renderError := application.dependencies.RenderComparison(matrix.ComparisonPageData{Comparison: &comparison})
	if renderError != nil {
		return fmt.Errorf(renderErrorFormat, renderError)
	}

	if writeError := application.dependencies.WriteOutputFile(configuration.OutputPath, pageHTML); writeError != nil {
		return writeError
	}

	fmt.Fprintf(application.dependencies.Stdout, writeSuccessMessageFormat+"\n", configuration.OutputPath)
	return nil
}

func newDefaultDumpDependencies() DumpDependencies {
	return DumpDependencies{
		ReadArchive: matrix.ReadTwitterZip,
		BuildResolver: func() (matrix.AccountHandleResolver, error) {
			return handles.NewResolver(handles.Config{})
		},
		RenderComparison: matrix.RenderComparisonPage,
		WriteOutputFile:  defaultWriteOutputFile,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
	}
}

func defaultWriteOutputFile(outputPath string, contents string) error {
	file, createError := os.Create(outputPath)
	if createError != nil {
		return fmt.Errorf(createFileErrorFormat, outputPath, createError)
	}
	defer file.Close()

	if _, writeError := file.WriteString(contents); writeError != nil {
		return fmt.Errorf(writeFileErrorFormat, outputPath, writeError)
	}
	return nil
}
