package dyn

import "slices"

// SensitiveValueRedacted is the placeholder emitted in JSON/YAML output
// whenever a sensitive string value is serialized.
const SensitiveValueRedacted = "<redacted>"

// secretString is the internal storage type for sensitive string values.
// Storing a distinct Go type (rather than a plain string) makes sensitivity
// impossible to strip by accident: every code path that copies v.v preserves
// it, and every switch on v.v that handles `string` but not `secretString`
// fails to compile or silently falls through — making omissions auditable.
//
// Kind() still returns KindString for a secretString value so that all
// existing switch-on-Kind logic continues to work unchanged. Only the
// leaf accessors (AsAny, AsString, MustString) are aware of the type.
type secretString struct {
	value string
}

// NewSensitiveValue returns a new KindString Value whose content is treated as
// sensitive. JSON and YAML serializers replace it with [SensitiveValueRedacted];
// AsString / MustString still return the real value for use in the deployment
// pipeline.
func NewSensitiveValue(s string, loc []Location) Value {
	return Value{
		v: secretString{s},
		k: KindString,
		l: slices.Clone(loc),
	}
}
