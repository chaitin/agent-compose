package loaders

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastschema/qjs"
)

type jsValueEncoder struct {
	context    *qjs.Context
	jsonObject *qjs.Value
	stringify  *qjs.Value
}

func newJSValueEncoder(jsctx *qjs.Context) (*jsValueEncoder, error) {
	jsonObject := jsctx.Global().GetPropertyStr("JSON")
	if jsonObject == nil || !jsonObject.IsObject() {
		return nil, fmt.Errorf("initialize js value encoder: JSON object is unavailable")
	}
	stringify := jsonObject.GetPropertyStr("stringify")
	if stringify == nil || !stringify.IsFunction() {
		return nil, fmt.Errorf("initialize js value encoder: JSON.stringify is unavailable")
	}
	// qjs v0.0.6's Value.JSONStringify wrapper frees the QuickJS C string before
	// Go reads it. Keep the JS intrinsic itself and return a managed JS string
	// across the WASM boundary instead. Capturing it before user code runs also
	// prevents a script from changing the engine's serialization behavior.
	return &jsValueEncoder{
		context:    jsctx,
		jsonObject: jsonObject,
		stringify:  stringify,
	}, nil
}

func (e *jsValueEncoder) Encode(value *qjs.Value) (string, error) {
	if value == nil || value.IsUndefined() {
		return "", nil
	}
	if value.IsNull() {
		return "null", nil
	}
	if value.IsBool() {
		if value.Bool() {
			return "true", nil
		}
		return "false", nil
	}
	if value.IsString() || value.IsBigInt() {
		data, err := json.Marshal(value.String())
		if err != nil {
			return "", fmt.Errorf("encode js string value: %w", err)
		}
		return string(data), nil
	}
	if value.IsNumber() {
		raw := strings.TrimSpace(value.String())
		if raw == "" {
			return "", nil
		}
		if raw == "NaN" || raw == "Infinity" || raw == "-Infinity" {
			data, err := json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("encode js numeric sentinel: %w", err)
			}
			return string(data), nil
		}
		if json.Valid([]byte(raw)) {
			return raw, nil
		}
	}

	jsonValue, err := e.context.Invoke(e.stringify, e.jsonObject, value)
	if err != nil {
		return "", fmt.Errorf("stringify js value: %w", err)
	}
	defer jsonValue.Free()
	if jsonValue.IsUndefined() {
		return "", nil
	}
	if !jsonValue.IsString() {
		return "", fmt.Errorf("stringify js value returned %s instead of a string", jsonValue.Type())
	}
	raw := strings.TrimSpace(jsonValue.String())
	if raw == "" || !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("stringify js value returned invalid JSON")
	}
	return raw, nil
}

func loaderResultJSON(encoder *jsValueEncoder, value *qjs.Value) (string, bool, error) {
	if value == nil || value.IsUndefined() {
		return "", false, nil
	}
	jsonValue, err := encoder.Encode(value)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(jsonValue) == "" {
		return "", false, nil
	}
	return jsonValue, true, nil
}
