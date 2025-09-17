package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
	"github.com/f-sync/fsync/internal/server"
)

const (
	commandUse                    = "server"
	commandShortDescription       = "Serve the comparison matrix over HTTP"
	envPrefix                     = "FSYNC_SERVER"
	flagZipAName                  = "zip-a"
	flagZipADescription           = "Path to the first Twitter archive zip"
	flagZipBName                  = "zip-b"
	flagZipBDescription           = "Path to the second Twitter archive zip"
	flagResolveHandlesName        = "resolve-handles"
	flagResolveHandlesDescription = "Resolve missing handles via HTTPS lookups"
	flagHostName                  = "host"
	flagHostDescription           = "Host interface for the HTTP server"
	flagPortName                  = "port"
	flagPortDescription           = "Port for the HTTP server"
	defaultHost                   = "127.0.0.1"
	defaultPort                   = 8080
	errMessageMissingZipFlags     = "both --zip-a and --zip-b must be provided"
	errMessageLoggerCreate        = "create logger"
	errMessageReadArchive         = "read archive"
	errMessageResolverCreate      = "create resolver"
	errMessageListenAndServe      = "listen and serve"
	logMessageLoadingArchive      = "loading twitter archive"
	logMessageResolvingHandles    = "resolving handles"
	logMessageHandleError         = "handle resolution error"
	logMessageStartingServer      = "starting HTTP server"
	logMessageServerStopped       = "server stopped"
	logMessageListenError         = "server listen failure"
	logFieldArchivePath           = "archive"
	logFieldAccountID             = "account_id"
	logFieldAddress               = "address"
)

func main() {
	cobra.CheckErr(newServerCommand().Execute())
}

func newServerCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   commandUse,
		Short: commandShortDescription,
		RunE:  runServerCommand,
	}

	command.Flags().String(flagZipAName, "", flagZipADescription)
	command.Flags().String(flagZipBName, "", flagZipBDescription)
	command.Flags().Bool(flagResolveHandlesName, false, flagResolveHandlesDescription)
	command.Flags().String(flagHostName, defaultHost, flagHostDescription)
	command.Flags().Int(flagPortName, defaultPort, flagPortDescription)

	bindFlagToViper(command, flagZipAName)
	bindFlagToViper(command, flagZipBName)
	bindFlagToViper(command, flagResolveHandlesName)
	bindFlagToViper(command, flagHostName)
	bindFlagToViper(command, flagPortName)

	cobra.OnInitialize(configureEnvironment)

	return command
}

func bindFlagToViper(command *cobra.Command, flagName string) {
	cobra.CheckErr(viper.BindPFlag(flagName, command.Flags().Lookup(flagName)))
}

func configureEnvironment() {
	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}

func runServerCommand(*cobra.Command, []string) error {
	zipPathA := strings.TrimSpace(viper.GetString(flagZipAName))
	zipPathB := strings.TrimSpace(viper.GetString(flagZipBName))
	if zipPathA == "" || zipPathB == "" {
		return fmt.Errorf(errMessageMissingZipFlags)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("%s: %w", errMessageLoggerCreate, err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info(logMessageLoadingArchive, zap.String(logFieldArchivePath, zipPathA))
	accountSetsA, ownerA, err := matrix.ReadTwitterZip(zipPathA)
	if err != nil {
		return fmt.Errorf("%s %s: %w", errMessageReadArchive, zipPathA, err)
	}

	logger.Info(logMessageLoadingArchive, zap.String(logFieldArchivePath, zipPathB))
	accountSetsB, ownerB, err := matrix.ReadTwitterZip(zipPathB)
	if err != nil {
		return fmt.Errorf("%s %s: %w", errMessageReadArchive, zipPathB, err)
	}

	if viper.GetBool(flagResolveHandlesName) {
		logger.Info(logMessageResolvingHandles)
		resolver, err := handles.NewResolver(handles.Config{})
		if err != nil {
			return fmt.Errorf("%s: %w", errMessageResolverCreate, err)
		}
		resolutionErrors := matrix.MaybeResolveHandles(context.Background(), resolver, true, &accountSetsA, &accountSetsB)
		for accountID, resolutionErr := range resolutionErrors {
			logger.Warn(logMessageHandleError, zap.String(logFieldAccountID, accountID), zap.Error(resolutionErr))
		}
	}

	router, err := server.NewRouter(server.RouterConfig{
		ComparisonData: &server.ComparisonData{
			AccountSetsA: accountSetsA,
			AccountSetsB: accountSetsB,
			OwnerA:       ownerA,
			OwnerB:       ownerB,
		},
		Logger: logger,
	})
	if err != nil {
		return err
	}

	host := viper.GetString(flagHostName)
	port := viper.GetInt(flagPortName)
	address := fmt.Sprintf("%s:%d", host, port)
	logger.Info(logMessageStartingServer, zap.String(logFieldAddress, address))

	httpServer := &http.Server{Addr: address, Handler: router}
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error(logMessageListenError, zap.Error(err))
		return fmt.Errorf("%s: %w", errMessageListenAndServe, err)
	}

	logger.Info(logMessageServerStopped)
	return nil
}
