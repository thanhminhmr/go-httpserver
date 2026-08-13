/*
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */

package httpserver

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/thanhminhmr/go-common/ctrl"
	"github.com/thanhminhmr/go-exception"
)

// ServerConfig configures the [http.Server] registered by [NewServer]. Timeout
// values are in seconds. [NewServer] does not apply or validate the
// configuration tags.
type ServerConfig struct {
	// Port is the TCP port to listen on all interfaces.
	Port uint16 `cfg:"port" validate:"required" default:"8080"`

	// ReadHeaderTimeout limits time spent reading request headers, in seconds.
	ReadHeaderTimeout int `cfg:"read_header_timeout" validate:"min=1,max=60" default:"5"`

	// IdleTimeout limits idle keep-alive time, in seconds.
	IdleTimeout int `cfg:"idle_timeout" validate:"min=1,max=3600" default:"60"`

	// MaxHeaderBytes limits request header size in bytes.
	MaxHeaderBytes int `cfg:"max_header_bytes" validate:"min=0,max=65536" default:"4096"`

	// ShutdownOnError cancels the application when serving fails unexpectedly.
	ShutdownOnError bool `cfg:"shutdown_on_error" default:"true"`
}

// NewServer creates an [http.ServeMux], installs request logging and panic
// recovery, and registers an [http.Server] with the [ctrl] lifecycle.
//
// The server listens on ":<config.Port>" when the lifecycle starts and shuts
// down during cleanup. The config must already be defaulted and validated, and
// should not be modified after this call. If serving fails unexpectedly,
// ShutdownOnError controls whether the application lifecycle is canceled.
func NewServer(logger *zerolog.Logger, config *ServerConfig) Router {
	// create route
	serveMux := http.NewServeMux()
	// start the server
	ctrl.Register(func(ctx, _ context.Context) (ctrl.Runner, ctrl.Cleaner) {
		// create the http server
		var server httpServer
		server = httpServer{
			config:   config,
			serveMux: serveMux,
			server: http.Server{
				Addr:              fmt.Sprintf(":%d", config.Port),
				Handler:           &server,
				ReadHeaderTimeout: time.Duration(config.ReadHeaderTimeout) * time.Second,
				IdleTimeout:       time.Duration(config.IdleTimeout) * time.Second,
				MaxHeaderBytes:    config.MaxHeaderBytes,
			},
		}
		// return the runner and the cleaner
		return server.runner, server.cleaner
	})
	// return the router
	return Router{serveMux: serveMux, logger: logger}
}

type httpServer struct {
	config   *ServerConfig
	serveMux *http.ServeMux
	server   http.Server
}

func (s *httpServer) runner(ctx context.Context, shutdown context.CancelFunc) {
	logger := zerolog.Ctx(ctx)
	logger.Info().Str("address", s.server.Addr).Msg("Start serving")
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error().Err(err).Msg("Server closed with error")
		if s.config.ShutdownOnError {
			shutdown()
		}
	}
}

func (s *httpServer) cleaner(ctx context.Context) {
	logger := zerolog.Ctx(ctx)
	logger.Info().Msg("Shutting down...")
	if err := s.server.Shutdown(ctx); err != nil {
		logger.Error().Err(err).Msg("Error while shutting down")
	}
	logger.Info().Msg("Shutdown complete")
}

func (s *httpServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	logger := zerolog.Ctx(request.Context()).With().
		Str("request_id", strconv.FormatUint(rand.Uint64(), 36)).Logger()
	// log request and response
	logger.Info().Str("method", request.Method).Str("host", request.Host).
		Str("path", request.URL.Path).Msg("Request")
	start := time.Now()
	wrappedWriter := newResponseWriterTracker(writer)
	defer func(start time.Time, wrappedWriter *responseWriterTracker) {
		duration := time.Since(start)
		logger.Info().Int("status", wrappedWriter.Status()).
			Int("bytes", wrappedWriter.BytesWritten()).
			Dur("duration", duration).
			Msg("Response")
	}(start, wrappedWriter)
	// recover any panic
	defer exception.Recover(func(recovered exception.Exception) {
		logger.Error().Any("recovered", recovered).Msg("Recovered from panic")
		// response with 500 Internal Server Error
		if wrappedWriter.Status() == 0 {
			wrappedWriter.WriteHeader(http.StatusInternalServerError)
		}
	})
	// call the serveMux handler
	s.serveMux.ServeHTTP(wrappedWriter, request.WithContext(logger.WithContext(request.Context())))
}
