// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/go-stomp/stomp/v3/frame"
)

var benchFrameSink *frame.Frame

func BenchmarkFrameFanOutClone(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 4096)
	f := frame.New(
		frame.MESSAGE,
		frame.Destination, "/topic/bench",
		frame.ContentLength, strconv.Itoa(len(body)),
		frame.ContentType, "application/json;charset=UTF-8")
	f.Body = body

	for _, subscribers := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("%d-subscribers/deep", subscribers), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for sub := 0; sub < subscribers; sub++ {
					cloned := f.Clone()
					cloned.Header.Add(frame.Subscription, strconv.Itoa(sub))
					benchFrameSink = cloned
				}
			}
		})
		b.Run(fmt.Sprintf("%d-subscribers/headers", subscribers), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for sub := 0; sub < subscribers; sub++ {
					cloned := cloneFrameHeaders(f)
					cloned.Header.Add(frame.Subscription, strconv.Itoa(sub))
					benchFrameSink = cloned
				}
			}
		})
	}
}
