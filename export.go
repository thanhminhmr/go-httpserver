/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import "net/http"

// Middleware is the standard net/http middleware function shape.
type Middleware = func(http.Handler) http.Handler

// KeyValue holds all URL parameters for an empty `url:""` tag.
type KeyValue = map[string]string

// KeyValues holds all values for an empty `cookie:""`, `query:""`, or
// `form:""` tag.
type KeyValues = map[string][]string
