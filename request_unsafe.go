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

type httpSegment struct {
	s     string
	wild  bool
	multi bool
}

type httpPattern struct {
	str      string
	method   string
	host     string
	segments []httpSegment
	loc      string
}

type httpRequest struct {
	_ [33]uintptr

	// The following fields are for requests matched by ServeMux.
	pat     *httpPattern // the pattern that matched
	matches []string     // values for the matching wildcards in pat
}

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
