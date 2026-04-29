// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"github.com/google/uuid"
	"sync/atomic"
)

type channelEventHandler struct {
	callBackFunction        MessageHandlerFunction
	contextCallBackFunction MessageHandlerContextFunction
	runOnce                 bool
	fired                   atomic.Bool
	uuid                    *uuid.UUID
}
