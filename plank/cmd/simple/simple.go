// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
    "github.com/pb33f/ranch/plank/pkg/server"
    "github.com/pb33f/ranch/plank/services"
    "github.com/pb33f/ranch/plank/utils"
    "os"
)

// configure flags
func main() {
    serverConfig, err := server.CreateServerConfig()
    if err != nil {
        utils.Log.Fatalln(err)
    }
    platformServer := server.NewPlatformServer(serverConfig)
    if err = platformServer.RegisterService(services.NewPingPongService(), services.PingPongServiceChan); err != nil {
        utils.Log.Fatalln(err)
    }
    syschan := make(chan os.Signal, 1)
    platformServer.StartServer(syschan)
}
