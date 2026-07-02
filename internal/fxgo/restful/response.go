package restful

type StrStatusResp[T any] struct {
	Err  *string `json:"err"`
	Msg  string  `json:"msg"`
	Data T       `json:"data"`
}

type CodeStatusResp[T any] struct {
	Code uint32 `json:"code"` // align with https://pkg.go.dev/google.golang.org/grpc/codes
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type ResponseType[T any] interface {
	StrStatusResp[T] | CodeStatusResp[T]
}

func NewResponse[T any, R ResponseType[T]](code any, msg string, data T) R {
	var zero R
	switch any(zero).(type) {
	case StrStatusResp[T]:
		ret := StrStatusResp[T]{
			Err:  ExtractCodeString(code),
			Msg:  msg,
			Data: data,
		}
		return any(ret).(R)
	case CodeStatusResp[T]:
		ret := CodeStatusResp[T]{
			Code: ExtractCodeUint32(code),
			Msg:  msg,
			Data: data,
		}
		return any(ret).(R)
	default:
		return zero
	}
}
