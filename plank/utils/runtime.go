// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package utils

import (
	"runtime"
	"strings"
)

// GetGoRoutineID returns the current goroutine ID parsed from the runtime stack header.
func GetGoRoutineID() string {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idStr := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	return idStr
}

// GetCurrentStackFrame returns the stack frame for its caller.
func GetCurrentStackFrame() runtime.Frame {
	return getFrame(1)
}

// GetCallerStackFrame returns the stack frame for its caller's caller.
func GetCallerStackFrame() runtime.Frame {
	return getFrame(2)
}

func getFrame(skipFrames int) runtime.Frame {
	targetFrameIdx := skipFrames + 2
	programCounters := make([]uintptr, targetFrameIdx+2)
	n := runtime.Callers(0, programCounters)
	frame := runtime.Frame{Function: "unknown"}

	if n > 0 {
		frames := runtime.CallersFrames(programCounters[:n])
		for more, frameIdx := true, 0; more && frameIdx <= targetFrameIdx; frameIdx++ {
			var frameIdxCandidate runtime.Frame
			frameIdxCandidate, more = frames.Next()
			if frameIdx == targetFrameIdx {
				frame = frameIdxCandidate
				break
			}
		}
	}
	return frame
}
