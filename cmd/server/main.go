package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/f-sync/fsync/internal/cresolver"
	"github.com/f-sync/fsync/internal/server"
)

const (
	commandUse                       = "server"
	commandShortDescription          = "Serve the comparison matrix over HTTP"
	envPrefix                        = "FSYNC_SERVER"
	flagHostName                     = "host"
	flagHostDescription              = "Host interface for the HTTP server"
	flagPortName                     = "port"
	flagPortDescription              = "Port for the HTTP server"
	defaultHost                      = "127.0.0.1"
	defaultPort                      = 8080
	errMessageLoggerCreate           = "create logger"
	errMessageResolverCreate         = "create resolver"
	errMessageListenAndServe         = "listen and serve"
	logMessageResolverInitialization = "initializing handle resolver"
	logMessageStartingServer         = "starting HTTP server"
	logMessageServerStopped          = "server stopped"
	logMessageListenError            = "server listen failure"
	logFieldAddress                  = "address"
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

	command.Flags().String(flagHostName, defaultHost, flagHostDescription)
	command.Flags().Int(flagPortName, defaultPort, flagPortDescription)

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
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("%s: %w", errMessageLoggerCreate, err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info(logMessageResolverInitialization)
	resolver, resolverErr := cresolver.NewService(cresolver.Config{})
	if resolverErr != nil {
		return fmt.Errorf("%s: %w", errMessageResolverCreate, resolverErr)
	}

	router, err := server.NewRouter(server.RouterConfig{
		Logger:         logger,
		HandleResolver: resolver,
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
