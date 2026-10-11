package edges

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rt01Fixture is one case of RT-01 in the fixture format of the architecture document: a
// violation is a call that must be refused, a compliant case one that must be allowed.
type rt01Fixture struct {
	ID        string `json:"id"`
	Rule      string `json:"rule"`
	Direction string `json:"direction"`
	Call      struct {
		Temperature  *float64        `json:"temperature"`
		OutputSchema json.RawMessage `json:"output_schema"`
		// Output is the model's output as text: a fixture can then hold an output that is
		// no JSON.
		Output string `json:"output"`
	} `json:"call"`
	Expect struct {
		Result string `json:"result"`
		// Reason is which part of the rule refuses the call: temperature, schema or output.
		Reason string `json:"reason"`
	} `json:"expect"`
}

var reasons = map[string]error{"temperature": ErrTemperature, "schema": ErrSchema, "output": ErrOutput}

func loadRT01Fixtures(t *testing.T) []rt01Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rt01", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("Glob(testdata/rt01) = %d files, %v; want the fixtures", len(paths), err)
	}
	sort.Strings(paths)
	fixtures := make([]rt01Fixture, 0, len(paths))
	for i := 0; i < len(paths); i++ {
		body, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatalf("ReadFile(%s) = %v, want nil", paths[i], err)
		}
		var fixture rt01Fixture
		if err = json.Unmarshal(body, &fixture); err != nil {
			t.Fatalf("%s: %v", paths[i], err)
		}
		if fixture.ID+".json" != filepath.Base(paths[i]) || fixture.Rule != "rt01" {
			t.Fatalf("%s: id %q, rule %q; want the file's name and rt01", paths[i], fixture.ID, fixture.Rule)
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

// checkRT01Fixture replays one fixture and returns what is wrong with the outcome, "" when
// nothing is. It fails a violation that was allowed and a compliant call that was refused.
func checkRT01Fixture(fixture rt01Fixture) string {
	err := Check(Call{Temperature: fixture.Call.Temperature, OutputSchema: fixture.Call.OutputSchema, Output: []byte(fixture.Call.Output)})
	switch {
	case fixture.Direction == "violation" && err == nil:
		return "a violation was allowed"
	case fixture.Direction == "compliant" && err != nil:
		return "a compliant call was refused: " + err.Error()
	case (fixture.Expect.Result == "allowed") != (err == nil):
		return "expect.result does not match the direction"
	case err == nil && fixture.Expect.Reason != "":
		return "an allowed call names a reason"
	case err != nil && !errors.Is(err, reasons[fixture.Expect.Reason]):
		return "refused for another reason than " + fixture.Expect.Reason + ": " + err.Error()
	}
	return ""
}

func TestRT01Fixtures(t *testing.T) {
	fixtures := loadRT01Fixtures(t)
	directions := map[string]int{}
	refusedFor := map[string]int{}
	for i := 0; i < len(fixtures); i++ {
		fixture := fixtures[i]
		directions[fixture.Direction]++
		refusedFor[fixture.Expect.Reason]++
		t.Run(fixture.ID, func(t *testing.T) {
			if wrong := checkRT01Fixture(fixture); wrong != "" {
				t.Fatalf("%s (%s): %s", fixture.ID, fixture.Direction, wrong)
			}
		})
	}
	if directions["violation"] == 0 || directions["compliant"] == 0 || len(directions) != 2 {
		t.Fatalf("fixture directions = %v, want violations and compliant cases and nothing else", directions)
	}
	if refusedFor["temperature"] == 0 || refusedFor["schema"] == 0 || refusedFor["output"] == 0 {
		t.Fatalf("refusals by part = %v, want each part of the rule refused by a fixture", refusedFor)
	}
}

// The checker itself is checked: a fixture that claims the wrong outcome, or the wrong part
// of the rule, is reported.
func TestRT01CheckerRefusesAFixtureThatClaimsTheWrongOutcome(t *testing.T) {
	zero, hot := 0.0, 0.7
	schema := json.RawMessage(`{"type":"object","required":["verdict"]}`)
	planted := func(direction string, temperature *float64, output string, result string, reason string) rt01Fixture {
		var fixture rt01Fixture
		fixture.ID, fixture.Rule, fixture.Direction = "planted", "rt01", direction
		fixture.Call.Temperature, fixture.Call.OutputSchema, fixture.Call.Output = temperature, schema, output
		fixture.Expect.Result, fixture.Expect.Reason = result, reason
		return fixture
	}
	cases := []struct {
		name    string
		fixture rt01Fixture
	}{
		{"a hot call planted as compliant", planted("compliant", &hot, `{"verdict":"pass"}`, "allowed", "")},
		{"a sound call planted as a violation", planted("violation", &zero, `{"verdict":"pass"}`, "refused", "temperature")},
		{"a refusal blamed on the wrong part", planted("violation", &hot, `{"verdict":"pass"}`, "refused", "output")},
		{"a result that contradicts the direction", planted("violation", &hot, `{"verdict":"pass"}`, "allowed", "temperature")},
		{"an allowed call that names a reason", planted("compliant", &zero, `{"verdict":"pass"}`, "allowed", "output")},
	}
	for i := 0; i < len(cases); i++ {
		if wrong := checkRT01Fixture(cases[i].fixture); wrong == "" {
			t.Errorf("%s passed the checker, want it reported", cases[i].name)
		}
	}
	if wrong := checkRT01Fixture(planted("compliant", &zero, `{"verdict":"pass"}`, "allowed", "")); wrong != "" {
		t.Errorf("a sound fixture was reported: %s", wrong)
	}
}

func TestCheckBoundsAndSchemaShapes(t *testing.T) {
	zero, negativeZero, tiny := 0.0, 0.0, 1e-9
	negativeZero *= -1
	schema := json.RawMessage(`{"type":"string"}`)
	atBound := `"` + strings.Repeat("a", MaxDocumentBytes-2) + `"`
	cases := []struct {
		name string
		call Call
		want error
	}{
		{"a temperature of minus zero", Call{Temperature: &negativeZero, OutputSchema: schema, Output: []byte(`"ok"`)}, nil},
		{"the smallest temperature above zero", Call{Temperature: &tiny, OutputSchema: schema, Output: []byte(`"ok"`)}, ErrTemperature},
		{"an output exactly at the size bound", Call{Temperature: &zero, OutputSchema: schema, Output: []byte(atBound)}, nil},
		{"an output one byte above the bound", Call{Temperature: &zero, OutputSchema: schema, Output: []byte(atBound + " ")}, ErrOutput},
		{"an empty output", Call{Temperature: &zero, OutputSchema: schema, Output: nil}, ErrOutput},
		{"a schema that is a JSON array", Call{Temperature: &zero, OutputSchema: json.RawMessage(`[]`), Output: []byte(`"ok"`)}, ErrSchema},
		{"a schema that is the literal true", Call{Temperature: &zero, OutputSchema: json.RawMessage(`true`), Output: []byte(`"ok"`)}, ErrSchema},
		{"a schema that does not compile", Call{Temperature: &zero, OutputSchema: json.RawMessage(`{"type":"sentence"}`), Output: []byte(`"ok"`)}, ErrSchema},
		{"a schema above the size bound", Call{Temperature: &zero, OutputSchema: json.RawMessage(`{"description":"` + strings.Repeat("d", MaxDocumentBytes) + `"}`), Output: []byte(`"ok"`)}, ErrSchema},
	}
	for i := 0; i < len(cases); i++ {
		err := Check(cases[i].call)
		if cases[i].want == nil && err != nil {
			t.Errorf("%s: Check() = %v, want nil", cases[i].name, err)
		}
		if cases[i].want != nil && !errors.Is(err, cases[i].want) {
			t.Errorf("%s: Check() = %v, want %v", cases[i].name, err, cases[i].want)
		}
	}
	// A schema that is no object is told apart from an empty one: the caller reads which of
	// the two it sent.
	shapes := map[string]string{`[]`: "it is no JSON object", `true`: "it is no JSON object", `{}`: "an empty schema accepts every output"}
	for raw, want := range shapes {
		err := Check(Call{Temperature: &zero, OutputSchema: json.RawMessage(raw), Output: []byte(`"ok"`)})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("schema %s: Check() = %v, want it to say %q", raw, err, want)
		}
	}
}
