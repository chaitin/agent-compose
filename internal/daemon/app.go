package daemon

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/lmittmann/tint"
	"github.com/samber/do/v2"
	"google.golang.org/grpc/codes"

	agentcompose "agent-compose/pkg/agentcompose/service"
	"agent-compose/pkg/config"
	"agent-compose/pkg/fxgo/echofn"
	"agent-compose/pkg/fxgo/restful"
	"agent-compose/pkg/fxgo/utils"
	"agent-compose/pkg/health"
)

type Runner func(context.Context) error

type Options struct {
	LoadDotEnv      bool
	SetRlimit       bool
	StartBackground func(do.Injector) error
}

type App struct {
	DI              do.Injector
	Echo            *echo.Echo
	Logger          *slog.Logger
	Config          *config.Config
	startBackground func(do.Injector) error
	startOnce       sync.Once
	startErr        error
}

func NewEcho(di do.Injector) (*echo.Echo, error) {
	e := echo.New()
	e.HTTPErrorHandler = echofn.EchoHTTPErrorHandler
	e.JSONSerializer = echofn.NewEpochTimeJSONSerializer()
	conf := do.MustInvoke[*config.Config](di)

	e.GET("/api/version", func(c echo.Context) error {
		return c.JSON(200, restful.NewResponse[map[string]any, restful.StrStatusResp[map[string]any]](nil, codes.OK.String(), map[string]any{
			"version":   conf.Version,
			"timestamp": float64(time.Now().UnixNano()) / 1e9,
		}))
	})
	e.GET("/api/null", echofn.EchoWrap(restful.NullHandler[restful.StrStatusResp[any]]))
	return e, nil
}

func NewLogger(di do.Injector) (*slog.Logger, error) {
	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{NoColor: false, AddSource: true, TimeFormat: "2006-01-02_15:04:05.000"}))
	slog.SetDefault(logger)
	return logger, nil
}

func NewApp(ctx context.Context, opts Options) (*App, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.LoadDotEnv {
		if err := godotenv.Load(); err != nil {
			log.Printf("dotenv load skipped: %v", err)
		}
	}
	if opts.SetRlimit {
		if err := utils.SetRlimitNoFile(); err != nil {
			log.Printf("Warning: Failed to set RLIMIT_NOFILE: %v", err)
		}
	}

	di := do.New()
	do.ProvideValue(di, ctx)
	do.Provide(di, NewLogger)
	config.Setup(di)
	do.Provide(di, NewEcho)
	health.Setup(di)
	agentcompose.Register(di)

	app := do.MustInvoke[*echo.Echo](di)
	logger := do.MustInvoke[*slog.Logger](di)
	conf := do.MustInvoke[*config.Config](di)
	installMiddleware(app, conf)

	startBackground := opts.StartBackground
	if startBackground == nil {
		startBackground = agentcompose.StartBackground
	}
	return &App{
		DI:              di,
		Echo:            app,
		Logger:          logger,
		Config:          conf,
		startBackground: startBackground,
	}, nil
}

func (a *App) StartBackground() error {
	a.startOnce.Do(func() {
		a.startErr = a.startBackground(a.DI)
	})
	return a.startErr
}

func (a *App) Run(ctx context.Context) error {
	servers, err := a.listen()
	if err != nil {
		return err
	}

	if err := a.StartBackground(); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := servers.shutdown(shutdownCtx); shutdownErr != nil {
			err = errorsJoin(err, shutdownErr)
		}
		return err
	}

	serverErrCh := servers.serve(a.Logger)
	select {
	case err := <-serverErrCh:
		if err != nil {
			a.Logger.Error("agent-compose server failed", "error", err)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutdownErr := servers.shutdown(shutdownCtx); shutdownErr != nil {
				err = errorsJoin(err, shutdownErr)
			}
			return err
		}
	case <-ctx.Done():
		a.Logger.Info("shutdown requested", "error", ctx.Err())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := servers.shutdown(shutdownCtx); err != nil {
		a.Logger.Error("failed to shutdown agent-compose server", "error", err)
		return err
	}
	return nil
}

func Run(ctx context.Context) error {
	app, err := NewApp(ctx, Options{LoadDotEnv: true, SetRlimit: true})
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

func (a *App) listen() (*servers, error) {
	items := &servers{}

	unixListener, err := listenUnixSocket(a.Config.AgentComposeSocket)
	if err != nil {
		return nil, err
	}
	items.add("AGENT_COMPOSE_SOCKET", a.Config.AgentComposeSocket, unixListener, a.Echo, func() error {
		return os.Remove(a.Config.AgentComposeSocket)
	})

	if strings.TrimSpace(a.Config.HttpListen) != "" {
		tcpListener, err := netListen("tcp", a.Config.HttpListen)
		if err != nil {
			shutdownErr := items.shutdown(context.Background())
			return nil, errorsJoin(formatListenError(a.Config.HttpListen, err), shutdownErr)
		}
		items.add("HTTP_LISTEN", a.Config.HttpListen, tcpListener, a.Echo, nil)
	}

	return items, nil
}
