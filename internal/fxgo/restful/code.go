package restful

import (
	"google.golang.org/grpc/codes"
)

func ExtractCodeString(code any) *string {
	if code == nil {
		return nil
	}
	switch codeValue := code.(type) {
	case *string:
		return codeValue

	case string:
		return &codeValue

	case codes.Code:
		if codeValue == codes.OK {
			return nil
		}
		s := codeValue.String()
		return &s

	default:
		v := codes.Unknown.String()
		return &v
	}
}

func ExtractCodeUint32(code any) uint32 {
	if code == nil {
		return 0
	}
	switch codeValue := code.(type) {
	case uint32:
		return codeValue

	case codes.Code:
		return uint32(codeValue)

	case uint8:
		return uint32(codeValue)
	case int8:
		return uint32(codeValue)
	case uint16:
		return uint32(codeValue)
	case int16:
		return uint32(codeValue)
	case uint64:
		return uint32(codeValue)
	case int64:
		return uint32(codeValue)
	case int32:
		return uint32(codeValue)

	default:
		return uint32(codes.Unknown)
	}
}
