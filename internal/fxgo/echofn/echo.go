package echofn

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
)

func EchoWrap(h func(w http.ResponseWriter, r *http.Request)) echo.HandlerFunc {
	return func(c echo.Context) error {
		h(c.Response().Writer, c.Request())
		return nil
	}
}

func EchoHTTPErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	he, ok := err.(*echo.HTTPError)
	if ok {
		if he.Internal != nil {
			if herr, ok := he.Internal.(*echo.HTTPError); ok {
				he = herr
			}
		}
	} else {
		he = &echo.HTTPError{
			Code:    http.StatusInternalServerError,
			Message: http.StatusText(http.StatusInternalServerError),
		}
	}

	// Issue #1426
	code := he.Code
	msg := he.Message

	switch m := he.Message.(type) {
	case string:
		msg = fmt.Sprintf("%s, %s", m, err.Error())
	case json.Marshaler:
		// do nothing - this type knows how to format itself to JSON
	case error:
		msg = m.Error()
	}

	// Send response
	if c.Request().Method == http.MethodHead { // Issue #608
		err = c.NoContent(he.Code)
	} else {
		var errType string

		if code < http.StatusInternalServerError {
			errType = codes.Unknown.String()
		} else {
			errType = codes.Internal.String()
		}

		err = c.JSON(code, echo.Map{
			"err": &errType,
			"msg": msg,
		})
	}
	if err != nil {
		slog.Error("http error in error", "err", err)
	}
}
