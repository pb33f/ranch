// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
    "github.com/pb33f/ranch/plank/pkg/server"
    "github.com/pb33f/ranch/plank/services"
    "github.com/pb33f/ranch/plank/utils"
    "github.com/spf13/cobra"
    "os"
)

var version string

func main() {
    var serverConfig *server.PlatformServerConfig

    // define the root command - entry of our application
    app := &cobra.Command{
        Use:     "plank",
        Version: version,
        Short:   "Plank demo application",
    }

    // define a command that starts the Plank server
    startCmd := &cobra.Command{
        Use:   "start-server",
        Short: "Start Plank server",
        RunE: func(cmd *cobra.Command, args []string) error {
            var platformServer server.PlatformServer
            platformServer = server.NewPlatformServer(serverConfig)

            // register services

            // ping-pong service
            if err := platformServer.RegisterService(services.NewPingPongService(),
                services.PingPongServiceChan); err != nil {
                return err
            }

            // stock ticker service
            if err := platformServer.RegisterService(services.NewStockTickerService(),
                services.StockTickerServiceChannel); err != nil {
                return err
            }

            // simple stream service
            if err := platformServer.RegisterService(services.NewSimpleStreamService(),
                services.SimpleStreamServiceChannel); err != nil {
                return err
            }

            // joke service
            if err := platformServer.RegisterService(services.NewJokeService(),
                services.JokeServiceChannel); err != nil {
                return err
            }

            // start server
            sysChan := make(chan os.Signal, 1)
            platformServer.StartServer(sysChan)

            return nil
        },
    }

    // create a new server configuration. this Cobra variant of the server.CreateServerConfig() function
    // configures and parses flags from the command line arguments into Cobra Command's structure. otherwise,
    // it is identical to server.CreateServerConfig() which you can use if you don't want to use Cobra.
    serverConfig, err := server.CreateServerConfigForCobraCommand(startCmd)
    if err != nil {
        utils.Log.Fatalln(err)
    }

    // add startCmd command to app
    app.AddCommand(startCmd)

    // start the app
    if err = app.Execute(); err != nil {
        utils.Log.Fatalln(err)
    }
}
