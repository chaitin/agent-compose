package echofn

import (
	"time"
	"unsafe"

	jsoniter "github.com/json-iterator/go"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// EpochTimeJSONSerializer implements echo.JSONSerializer and encodes time.Time
// values as float64 seconds since epoch.
type EpochTimeJSONSerializer struct {
	api jsoniter.API
}

func newEpochTimeAPI() jsoniter.API {
	// Register custom encoder for time.Time globally to output seconds since epoch as float64.
	jsoniter.RegisterTypeEncoderFunc("time.Time",
		func(ptr unsafe.Pointer, stream *jsoniter.Stream) {
			t := *(*time.Time)(ptr)
			sec := float64(t.UnixNano()) / 1e9
			stream.WriteFloat64(sec)
		},
		func(ptr unsafe.Pointer) bool {
			return false
		},
	)
	// Register custom decoder for time.Time to accept float64 seconds since epoch.
	jsoniter.RegisterTypeDecoderFunc("time.Time",
		func(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
			// Accept numeric seconds with fractional part (float64).
			f := iter.ReadFloat64()
			// On error, iterator stores it; caller will receive decode error.
			if iter.Error != nil {
				return
			}
			// Convert seconds to time.Time with nanosecond precision.
			ns := int64(f * 1e9)
			*(*time.Time)(ptr) = time.Unix(0, ns)
		},
	)

	// Register encoder/decoder for BSON DateTime to use float64 seconds.
	// Note: jsoniter keys by reflect.Type.String(), which for this type is "bson.DateTime".
	jsoniter.RegisterTypeEncoderFunc("bson.DateTime",
		func(ptr unsafe.Pointer, stream *jsoniter.Stream) {
			dt := *(*bson.DateTime)(ptr)
			t := dt.Time()
			sec := float64(t.UnixNano()) / 1e9
			stream.WriteFloat64(sec)
		},
		func(ptr unsafe.Pointer) bool {
			return false
		},
	)
	jsoniter.RegisterTypeDecoderFunc("bson.DateTime",
		func(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
			f := iter.ReadFloat64()
			if iter.Error != nil {
				return
			}
			ns := int64(f * 1e9)
			t := time.Unix(0, ns)
			*(*bson.DateTime)(ptr) = bson.NewDateTimeFromTime(t)
		},
	)

	// bson.D implements json.Marshaler and uses encoding/json internally, which would
	// serialize bson.DateTime/time.Time as RFC3339 strings. Override it to ensure
	// our custom time encoders apply even for nested MongoDB documents.
	jsoniter.RegisterTypeEncoderFunc("bson.D",
		func(ptr unsafe.Pointer, stream *jsoniter.Stream) {
			d := *(*bson.D)(ptr)
			if d == nil {
				stream.WriteNil()
				return
			}
			stream.WriteObjectStart()
			for i, e := range d {
				if i > 0 {
					stream.WriteMore()
				}
				stream.WriteObjectField(e.Key)
				stream.WriteVal(e.Value)
			}
			stream.WriteObjectEnd()
		},
		func(ptr unsafe.Pointer) bool {
			return false
		},
	)
	// Use config compatible with encoding/json behavior.
	return jsoniter.ConfigCompatibleWithStandardLibrary
}

func (s EpochTimeJSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	var (
		b   []byte
		err error
	)
	if indent != "" {
		b, err = s.api.MarshalIndent(i, "", indent)
	} else {
		b, err = s.api.Marshal(i)
	}
	if err != nil {
		return err
	}
	// Match encoding/json Encoder.Encode behavior by appending newline
	b = append(b, '\n')
	_, err = c.Response().Write(b)
	return err
}

func (s EpochTimeJSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	// Use json-iterator decoder so custom time.Time decoder applies.
	dec := s.api.NewDecoder(c.Request().Body)
	return dec.Decode(i)
}

func NewEpochTimeJSONSerializer() EpochTimeJSONSerializer {
	return EpochTimeJSONSerializer{api: newEpochTimeAPI()}
}
