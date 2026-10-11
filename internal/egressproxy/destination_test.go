package egressproxy

import (
	"strings"
	"testing"
)

func TestParseDestinationPositive(t *testing.T) {
	tests := []struct {
		in   string
		want Destination
	}{
		{"gateway.example:443", Destination{Host: "gateway.example", Port: 443}},
		{"Gateway.Example.:8443", Destination{Host: "gateway.example", Port: 8443}},
		{"localhost:4000", Destination{Host: "localhost", Port: 4000}},
		{"xn--bcher-kva.example:443", Destination{Host: "xn--bcher-kva.example", Port: 443}},
		{"127.0.0.1:4000", Destination{Host: "127.0.0.1", Port: 4000, Literal: true}},
		{"[::1]:4000", Destination{Host: "::1", Port: 4000, Literal: true}},
		{"[2001:DB8::1]:443", Destination{Host: "2001:db8::1", Port: 443, Literal: true}},
		{"[::ffff:192.0.2.7]:443", Destination{Host: "192.0.2.7", Port: 443, Literal: true}},
	}
	for i := 0; i < len(tests); i++ {
		got, err := ParseDestination(tests[i].in)
		if err != nil || got != tests[i].want {
			t.Errorf("ParseDestination(%q) = %+v, %v, want %+v", tests[i].in, got, err, tests[i].want)
		}
	}
}

func TestParseDestinationNegative(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"gateway.example", "want host:port"},
		{"gateway.example:", "is not 1.."},
		{":443", "host name must have"},
		{"gateway.example:https", "is not 1.."},
		{"gateway.example:+443", "is not 1.."},
		{"gateway.example:0443x", "is not 1.."},
		{"*.example:443", "host name label"},
		{"gate_way.example:443", "host name label"},
		{"-gateway.example:443", "host name label"},
		{"gateway-.example:443", "host name label"},
		{"gateway..example:443", "host name label"},
		{"gateway.example..:443", "host name label"},
		{"bücher.example:443", "host name label"},
		{"0.0.0.0:443", "unspecified address"},
		{"[::]:443", "unspecified address"},
		{"[fe80::1%eth0]:443", "with a zone"},
		{"127.1:443", "ends in the number"},
		{"2130706433:443", "ends in the number"},
		{"gateway.example.7:443", "ends in the number"},
		{"http://gateway.example:443", "want host:port"},
		{"gateway.example:443/path", "is not 1.."},
		{"", "want host:port"},
	}
	for i := 0; i < len(tests); i++ {
		got, err := ParseDestination(tests[i].in)
		if err == nil || !strings.Contains(err.Error(), tests[i].want) {
			t.Errorf("ParseDestination(%q) = %+v, %v, want an error with %q", tests[i].in, got, err, tests[i].want)
		}
	}
}

func TestParseDestinationBoundary(t *testing.T) {
	label63 := strings.Repeat("a", maxLabelLength)
	// 3 labels of 63 and one of 61 with their dots are 253 characters.
	host253 := strings.Join([]string{label63, label63, label63, strings.Repeat("b", 61)}, ".")
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"port 1", "gateway.example:1", true},
		{"port 0", "gateway.example:0", false},
		{"port 65535", "gateway.example:65535", true},
		{"port 65536", "gateway.example:65536", false},
		{"port with six digits", "gateway.example:000443", false},
		{"label of 63", label63 + ".example:443", true},
		{"label of 64", label63 + "a.example:443", false},
		{"host of 253", host253 + ":443", true},
		{"host of 254", host253 + "b:443", false},
	}
	for i := 0; i < len(tests); i++ {
		_, err := ParseDestination(tests[i].in)
		if (err == nil) != tests[i].ok {
			t.Errorf("ParseDestination(%s) = %v, want ok=%v", tests[i].name, err, tests[i].ok)
		}
	}
}

func TestCanonicalAcceptsOnlyTheOneSpelling(t *testing.T) {
	accepted := []string{"gateway.example:443", "127.0.0.1:4000", "[::1]:4000", "[2001:db8::1]:443"}
	for i := 0; i < len(accepted); i++ {
		if err := Canonical(accepted[i]); err != nil {
			t.Errorf("Canonical(%q) = %v, want nil", accepted[i], err)
		}
	}
	refused := []struct {
		in   string
		want string
	}{
		{"Gateway.example:443", `write it as "gateway.example:443"`},
		{"gateway.example.:443", `write it as "gateway.example:443"`},
		{"gateway.example:0443", `write it as "gateway.example:443"`},
		{"[2001:DB8::1]:443", `write it as "[2001:db8::1]:443"`},
		{"[::ffff:192.0.2.7]:443", `write it as "192.0.2.7:443"`},
		{"*.example:443", "host name label"},
	}
	for i := 0; i < len(refused); i++ {
		err := Canonical(refused[i].in)
		if err == nil || !strings.Contains(err.Error(), refused[i].want) {
			t.Errorf("Canonical(%q) = %v, want an error with %q", refused[i].in, err, refused[i].want)
		}
	}
}

func TestDestinationStringRoundTrips(t *testing.T) {
	values := []string{"gateway.example:443", "127.0.0.1:4000", "[::1]:4000"}
	for i := 0; i < len(values); i++ {
		dest, err := ParseDestination(values[i])
		if err != nil || dest.String() != values[i] {
			t.Errorf("ParseDestination(%q).String() = %q, %v, want the input", values[i], dest.String(), err)
		}
	}
}
