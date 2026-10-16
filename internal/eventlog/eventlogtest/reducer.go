// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

// Package eventlogtest provides test helpers for eventlog reducers.
package eventlogtest

import (
	"strings"
	"testing"

	"github.com/tekmora/epitrop/internal/eventlog"
)

// Reduce applies records in order with reducer and fails the test on the first error.
func Reduce(t *testing.T, reducer eventlog.Reducer, recs ...eventlog.Record) eventlog.State {
	t.Helper()
	st := eventlog.State{}
	for i := 0; i < len(recs); i++ {
		next, err := reducer(st, recs[i])
		if err != nil {
			t.Fatalf("Reducer(seq %d) = %v, want nil", recs[i].Seq, err)
		}
		st = next
	}
	return st
}

// AssertRejects checks that reducer returns an error containing want when given rec.
func AssertRejects(t *testing.T, reducer eventlog.Reducer, rec eventlog.Record, want string) {
	t.Helper()
	_, err := reducer(eventlog.State{}, rec)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Reducer() = %v, want an error holding %q", err, want)
	}
}
