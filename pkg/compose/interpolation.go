package compose

import (
	"fmt"
	"os"
	"strings"
)

func normalizeEnvVarMap(path string, values map[string]EnvVarSpec, options NormalizeOptions) (map[string]EnvVarSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make(map[string]EnvVarSpec, len(values))
	for key, value := range values {
		interpolated, err := interpolateEnvValue(joinPath(path, key)+".value", value.Value, options)
		if err != nil {
			return nil, err
		}
		value.Value = interpolated
		normalized[key] = value
	}
	return normalized, nil
}

func interpolateEnvValue(path string, value string, options NormalizeOptions) (string, error) {
	if options.LiteralValues {
		return value, nil
	}
	matches := envReferencePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	var b strings.Builder
	b.Grow(len(value))
	last := 0
	for _, match := range matches {
		b.WriteString(value[last:match[0]])
		name := value[match[2]:match[3]]
		envValue, ok := lookupInterpolationEnv(name, options)
		if !ok {
			return "", &ValidationError{Path: path, Message: fmt.Sprintf("environment variable %s is required", name)}
		}
		b.WriteString(envValue)
		last = match[1]
	}
	b.WriteString(value[last:])
	return b.String(), nil
}

// interpolateEnvValueLoose resolves environment references like
// interpolateEnvValue, but leaves a reference unresolved when its variable is
// missing from the environment instead of failing. It is used for source
// credential fields. Any remaining reference is literal after transport.
func interpolateEnvValueLoose(path string, value string, options NormalizeOptions) (string, error) {
	if options.LiteralValues {
		return value, nil
	}
	matches := envReferencePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	var b strings.Builder
	b.Grow(len(value))
	last := 0
	for _, match := range matches {
		b.WriteString(value[last:match[0]])
		name := value[match[2]:match[3]]
		envValue, ok := lookupInterpolationEnv(name, options)
		if ok {
			b.WriteString(envValue)
		} else {
			b.WriteString(value[match[0]:match[1]])
		}
		last = match[1]
	}
	b.WriteString(value[last:])
	return b.String(), nil
}

func lookupInterpolationEnv(name string, options NormalizeOptions) (string, bool) {
	if options.Env != nil {
		value, ok := options.Env[name]
		return value, ok
	}
	return os.LookupEnv(name)
}
