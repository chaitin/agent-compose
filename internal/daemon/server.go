package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
)

type server struct {
	name     string
	value    string
	listener net.Listener
	server   *http.Server
	cleanup  func() error
}

type servers struct {
	items []*server
}

var netListen = net.Listen

func (s *servers) add(name, value string, listener net.Listener, handler http.Handler, cleanup func() error) {
	httpServer := &http.Server{Handler: handler}
	if listener.Addr().Network() == "unix" {
		httpServer.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
			if isTrustedUnixSocketConn(conn) {
				return context.WithValue(ctx, localUnixSocketRequestKey{}, true)
			}
			return ctx
		}
	}
	s.items = append(s.items, &server{
		name:     name,
		value:    value,
		listener: listener,
		server:   httpServer,
		cleanup:  cleanup,
	})
}

func (s *servers) serve(logger *slog.Logger) <-chan error {
	errCh := make(chan error, len(s.items))
	for _, item := range s.items {
		go func(item *server) {
			logger.Info("agent-compose listener started", "config", item.name, "addr", item.listener.Addr().String())
			err := item.server.Serve(item.listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s %q: %w", item.name, item.value, err)
				return
			}
			errCh <- nil
		}(item)
	}
	return errCh
}

func (s *servers) shutdown(ctx context.Context) error {
	var joined error
	for _, item := range s.items {
		if err := item.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			joined = errors.Join(joined, fmt.Errorf("shutdown %s %q: %w", item.name, item.value, err))
		}
		if err := item.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			joined = errors.Join(joined, fmt.Errorf("close %s %q: %w", item.name, item.value, err))
		}
		if item.cleanup != nil {
			if err := item.cleanup(); err != nil && !errors.Is(err, os.ErrNotExist) {
				joined = errors.Join(joined, fmt.Errorf("cleanup %s %q: %w", item.name, item.value, err))
			}
		}
	}
	return joined
}

func errorsJoin(errs ...error) error {
	return errors.Join(errs...)
}

func formatListenError(addr string, err error) error {
	return fmt.Errorf("listen HTTP_LISTEN %q: %w", addr, err)
}
