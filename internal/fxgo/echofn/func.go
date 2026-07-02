package echofn

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"github.com/samber/mo"
	"github.com/samber/oops"
	"google.golang.org/grpc/codes"

	"agent-compose/internal/fxgo/restful"
)

func SendResultAsJson[T any, RT restful.ResponseType[T]](c echo.Context, result mo.Result[T]) error {
	data, err := result.Get()

	if err != nil {
		oopsErr, ok := err.(oops.OopsError)
		if ok {
			msg := oops.GetPublic(oopsErr, oopsErr.Error())
			code := oopsErr.Code()
			if code == nil {
				code = codes.Internal
			}

			c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			var zero T
			return c.JSON(http.StatusOK, restful.NewResponse[T, RT](code, msg, zero))
		} else {
			return err
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return c.JSON(http.StatusOK, restful.NewResponse[T, RT](nil, codes.OK.String(), data))
}

func ResultFunc2StrStatusResp[T any](h func(echo.Context) mo.Result[T]) echo.HandlerFunc {
	return func(c echo.Context) error {
		return SendResultAsJson[T, restful.StrStatusResp[T]](c, h(c))
	}
}

func ResultFunc2CodeStatusResp[T any](h func(echo.Context) mo.Result[T]) echo.HandlerFunc {
	return func(c echo.Context) error {
		return SendResultAsJson[T, restful.CodeStatusResp[T]](c, h(c))
	}
}

func DiResultFunc2StrStatusResp[T any](di do.Injector, h func(do.Injector, echo.Context) mo.Result[T]) echo.HandlerFunc {
	return func(c echo.Context) error {
		return SendResultAsJson[T, restful.StrStatusResp[T]](c, h(di, c))
	}
}

func DiResultFunc2CodeStatusResp[T any](di do.Injector, h func(do.Injector, echo.Context) mo.Result[T]) echo.HandlerFunc {
	return func(c echo.Context) error {
		return SendResultAsJson[T, restful.CodeStatusResp[T]](c, h(di, c))
	}
}

func DiFunc2EchoHandler(di do.Injector, h func(do.Injector, echo.Context) error) echo.HandlerFunc {
	return func(c echo.Context) error {
		return h(di, c)
	}
}

func Bind[T any](c echo.Context) mo.Result[T] {
	var form T
	if err := c.Bind(&form); err != nil {
		return mo.Err[T](oops.Code(codes.InvalidArgument).Wrap(err))
	} else {
		return mo.Ok(form)
	}
}
