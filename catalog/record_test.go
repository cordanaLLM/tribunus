package catalog

import (
	"testing"
	"time"
)

func validRecord() Record {
	return Record{
		ModelID:    "cordana/auto",
		AccessPath: AccessGateway,
		Provenance: Provenance{
			Source:    "litellm-gateway",
			FetchedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
			Kind:      KindMeasured,
		},
	}
}

func TestRecordValidate_Positive(t *testing.T) {
	r := validRecord()
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRecordValidate_Negative(t *testing.T) {
	cases := map[string]func(Record) Record{
		"empty model id": func(r Record) Record {
			r.ModelID = ""
			return r
		},
		"bad access path": func(r Record) Record {
			r.AccessPath = "carrier-pigeon"
			return r
		},
		"empty provenance source": func(r Record) Record {
			r.Provenance.Source = ""
			return r
		},
		"bad provenance kind": func(r Record) Record {
			r.Provenance.Kind = "guessed"
			return r
		},
		"zero fetched_at": func(r Record) Record {
			r.Provenance.FetchedAt = time.Time{}
			return r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := mutate(validRecord())
			if err := r.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", name)
			}
		})
	}
}

// TestRecordValidate_Boundary exercises the field-is-absent contract: a
// record with every optional field left nil (fully unknown beyond the
// required identity fields) must still validate, because "absent + reason"
// is the documented shape for unknown data, not an error condition.
func TestRecordValidate_Boundary(t *testing.T) {
	r := validRecord()
	r.Absent = map[string]string{
		"price_in_per_m": "gateway /v1/models does not expose pricing",
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for all-optional-fields-absent record", err)
	}
}
