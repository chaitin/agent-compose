package timefn

import (
	"time"

	"github.com/samber/mo"
	"github.com/samber/oops"
	"google.golang.org/grpc/codes"
)

const (
	DateFormatWithHypen = "2006-01-02"
)

func ParseTimeInLocation(format string, datetime string, loc *time.Location) mo.Result[time.Time] {
	if datetime == "" {
		return mo.Err[time.Time](oops.Code(codes.InvalidArgument).New("date empty"))
	}
	if loc == nil {
		return mo.Err[time.Time](oops.Code(codes.InvalidArgument).New("timezone empty"))
	}

	value, err := time.ParseInLocation(format, datetime, loc)
	if err != nil {
		return mo.Err[time.Time](oops.Code(codes.InvalidArgument).New("invalid datetime format"))
	}
	return mo.Ok(value)
}

func ParseBeijingDate(date string) mo.Result[time.Time] {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}

	return ParseTimeInLocation(DateFormatWithHypen, date, loc)
}
