// Package edges holds the check of RT-01: a model call made by the control plane is
// deterministic. Its request carries a temperature of exactly 0, and its output is validated
// against the JSON Schema the caller declared for it.
//
// The check is bound to no live edge yet: the runtime makes no model call of its own so
// far. The first one has to pass every call through Check.
package edges

import (
	"encoding/json"
	"errors"
	"fmt"

	golusorisschema "github.com/golusoris/golusoris/jsonschema"
)

// MaxDocumentBytes bounds the schema and the output Check reads.
const MaxDocumentBytes = 1 << 20

var (
	// ErrTemperature says the request did not carry a temperature of 0.
	ErrTemperature = errors.New("edges: RT-01: the temperature is not 0")
	// ErrSchema says the call declared no usable output schema.
	ErrSchema = errors.New("edges: RT-01: the call declares no output schema")
	// ErrOutput says the output does not match the declared schema.
	ErrOutput = errors.New("edges: RT-01: the output does not match its schema")
)

// Call is what RT-01 reads of one model call.
type Call struct {
	// Temperature is the sampling temperature the request carried, nil when it carried
	// none. A request without one runs at the provider's default, which is not 0.
	Temperature *float64 `json:"temperature"`
	// OutputSchema is the JSON Schema the caller declared for the output.
	OutputSchema json.RawMessage `json:"output_schema"`
	// Output is what the model returned, as it came.
	Output []byte `json:"-"`
}

// Check reports whether call meets RT-01. It returns ErrTemperature for a temperature that
// is missing or not 0, ErrSchema for a schema that is missing, is no JSON object, has no
// keyword or does not compile, and ErrOutput for an output that is no JSON or that the
// schema refuses. An empty schema is refused because it accepts every output: a call
// "validated" against it is not validated.
func Check(call Call) error {
	if call.Temperature == nil {
		return fmt.Errorf("%w: the request carries none", ErrTemperature)
	}
	if *call.Temperature != 0 {
		return fmt.Errorf("%w: it is %v", ErrTemperature, *call.Temperature)
	}
	schema, err := compileSchema(call.OutputSchema)
	if err != nil {
		return err
	}
	if len(call.Output) > MaxDocumentBytes {
		return fmt.Errorf("%w: it is larger than %d bytes", ErrOutput, MaxDocumentBytes)
	}
	if err = schema.Validate(call.Output); err != nil {
		return fmt.Errorf("%w: %w", ErrOutput, err)
	}
	return nil
}

// compileSchema refuses a schema that cannot validate anything and compiles the rest.
func compileSchema(raw json.RawMessage) (*golusorisschema.Schema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: none given", ErrSchema)
	}
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: it is larger than %d bytes", ErrSchema, MaxDocumentBytes)
	}
	var keywords map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keywords); err != nil {
		return nil, fmt.Errorf("%w: it is no JSON object: %w", ErrSchema, err)
	}
	if len(keywords) == 0 {
		return nil, fmt.Errorf("%w: an empty schema accepts every output", ErrSchema)
	}
	schema, err := golusorisschema.Compile("output.schema.json", raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSchema, err)
	}
	return schema, nil
}
