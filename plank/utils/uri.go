// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package utils

import (
	"regexp"
	"strings"
)

var protocolRegExp = regexp.MustCompile("https?://")

// SanitizeUrl removes excess forward slashes as well as pad the end of the URL with / if suffixSlash is true
func SanitizeUrl(url string, suffixSlash bool) string {
	if len(url) == 0 {
		return ""
	}
	strBuilder := strings.Builder{}
	isAtForwardSlash := false
	startLoc := 0
	protocolMatch := protocolRegExp.FindAllString(url, 1)
	if len(protocolMatch) > 0 {
		startLoc += len(protocolMatch[0])
		strBuilder.WriteString(protocolMatch[0])
	}

	for i := startLoc; i < len(url); i++ {
		c := url[i]
		if isAtForwardSlash && c == '/' {
			continue
		}
		isAtForwardSlash = c == '/'
		strBuilder.WriteByte(c)
	}

	sanitizedUrl := strBuilder.String()
	if suffixSlash && sanitizedUrl[len(sanitizedUrl)-1] != '/' {
		sanitizedUrl += "/"
	}

	if !suffixSlash && sanitizedUrl[len(sanitizedUrl)-1] == '/' {
		sanitizedUrl = sanitizedUrl[:len(sanitizedUrl)-1]
	}

	return sanitizedUrl
}
