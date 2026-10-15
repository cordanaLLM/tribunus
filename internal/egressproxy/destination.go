// Package egressproxy is the one way out of a sandbox that has no network. The server is an
// HTTP CONNECT proxy on a Unix socket: it runs outside the sandbox and reaches the declared
// destinations and nothing else. The relay runs inside the sandbox and gives the job a
// loopback port that leads to that socket, because the job's programs speak to a proxy
// over TCP. Every rule is on the server's side, where the job cannot change it.
package egressproxy

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	// MaxDestinations is how many destinations one job may declare.
	MaxDestinations = 16

	maxHostLength  = 253
	maxLabelLength = 63
	maxPort        = 65535
	maxPortDigits  = 5
)

// Destination is one place a job may reach: a host name or an address literal, and a port.
// Host is in its one spelling: a name in lower case without the trailing dot, an address
// as netip prints it.
type Destination struct {
	Host string
	Port int
	// Literal says that Host is an address. A literal is reached as written; a name is
	// resolved, and the addresses a name may not lead to are left out (see Server).
	Literal bool
}

// String is the destination as "host:port", an IPv6 address in brackets.
func (d Destination) String() string {
	return net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
}

// ParseDestination reads "host:port". It accepts any spelling of case and one trailing dot
// on a name, and returns the one spelling. It refuses a wildcard, a port outside 1..65535,
// the unspecified address, an address with a zone, and a name whose last label is a
// number, which a system resolver would read as an address.
func ParseDestination(value string) (Destination, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return Destination{}, fmt.Errorf("destination %q: want host:port", value)
	}
	port, err := parsePort(portText)
	if err != nil {
		return Destination{}, fmt.Errorf("destination %q: %w", value, err)
	}
	if addr, addrErr := netip.ParseAddr(host); addrErr == nil {
		if addr.Zone() != "" || addr.IsUnspecified() {
			return Destination{}, fmt.Errorf("destination %q: an address with a zone or the unspecified address is no destination", value)
		}
		return Destination{Host: addr.Unmap().String(), Port: port, Literal: true}, nil
	}
	name, err := canonicalName(host)
	if err != nil {
		return Destination{}, fmt.Errorf("destination %q: %w", value, err)
	}
	return Destination{Host: name, Port: port}, nil
}

// Canonical reports whether value is already the one spelling ParseDestination returns.
// Configuration accepts only that, so one destination has one line in every file.
func Canonical(value string) error {
	dest, err := ParseDestination(value)
	if err != nil {
		return err
	}
	if dest.String() != value {
		return fmt.Errorf("destination %q: write it as %q", value, dest.String())
	}
	return nil
}

func parsePort(text string) (int, error) {
	if text == "" || len(text) > maxPortDigits {
		return 0, fmt.Errorf("port %q is not 1..%d", text, maxPort)
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, fmt.Errorf("port %q is not 1..%d", text, maxPort)
		}
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > maxPort {
		return 0, fmt.Errorf("port %q is not 1..%d", text, maxPort)
	}
	return port, nil
}

func canonicalName(host string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "" || len(name) > maxHostLength {
		return "", fmt.Errorf("host name must have 1..%d characters", maxHostLength)
	}
	// The length of the name bounds the number of its labels.
	labels := strings.Split(name, ".")
	for i := 0; i < len(labels); i++ {
		if !validLabel(labels[i]) {
			return "", fmt.Errorf("host name label %q: want 1..%d of a-z, 0-9 and -, no - at either end", labels[i], maxLabelLength)
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return "", fmt.Errorf("host name ends in the number %q; write an address in full", labels[len(labels)-1])
	}
	return name, nil
}

func validLabel(label string) bool {
	if label == "" || len(label) > maxLabelLength {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		if !labelByte(label[i]) {
			return false
		}
	}
	return true
}

// labelByte reports whether c is allowed in a label.
func labelByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func allDigits(label string) bool {
	for i := 0; i < len(label); i++ {
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}
