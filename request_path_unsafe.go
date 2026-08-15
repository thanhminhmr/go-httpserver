/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"net/http"
	"unsafe"
)

// The types below mirror unexported net/http ServeMux/request state and are
// accessed through unsafe.Pointer. Their layout is coupled to the supported Go
// standard library. TestHTTPRequestUnsafeLayout is the guardrail: a Go upgrade
// must not be accepted until that test confirms these mirrors still match.

// httpSegment mirrors the unexported net/http ServeMux pattern segment used by
// getPathValues.
type httpSegment struct {
	s     string
	wild  bool
	multi bool
}

// httpPattern mirrors the unexported net/http ServeMux pattern representation
// needed to associate wildcard names with request matches.
type httpPattern struct {
	str      string
	method   string
	host     string
	segments []httpSegment
	loc      string
}

// httpRequest overlays the portion of http.Request needed to reach the matched
// ServeMux pattern and wildcard values. The leading padding is intentionally
// layout-dependent; see TestHTTPRequestUnsafeLayout.
type httpRequest struct {
	_ [33]uintptr

	// The following fields are for requests matched by ServeMux.
	pat     *httpPattern // the pattern that matched
	matches []string     // values for the matching wildcards in pat
}

// getPathValues returns all named wildcard values captured by the ServeMux
// pattern that matched r. It returns nil when no matched pattern is available.
func getPathValues(r *http.Request) KeyValue {
	request := (*httpRequest)(unsafe.Pointer(r))
	if request.pat == nil {
		return nil
	}
	keyValue := make(KeyValue, len(request.pat.segments))
	matchIndex := 0
	for _, segment := range request.pat.segments {
		if segment.wild && segment.s != "" {
			keyValue[segment.s] = request.matches[matchIndex]
			matchIndex++
		}
	}
	return keyValue
}
