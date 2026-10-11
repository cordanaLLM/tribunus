package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/egressproxy"
)

// proxyStopTimeout is how long the shim waits for the job's proxy to end its tunnels.
const proxyStopTimeout = 10 * time.Second

// jobProxy is the egress proxy of one job with network=proxy. The shim runs it outside
// the sandbox for as long as the job lives, so the rules of what the job reaches are in a
// process the job cannot touch. The zero value is a job without a proxy.
type jobProxy struct {
	server *egressproxy.Server
	relay  string
	cancel context.CancelFunc
	served chan error
	log    io.Writer
}

// startJobProxy starts the proxy for a job that declares destinations and returns the zero
// value for every other job. An error means the job must not start: it would run with no
// way to the destinations it was given.
func startJobProxy(ctx context.Context, cfg shimConfig, log io.Writer) (jobProxy, error) {
	sandbox := config.NormalizeSandbox(cfg.Sandbox)
	if sandbox.Mode != "enforce" || sandbox.Network != "proxy" {
		return jobProxy{}, nil
	}
	relay, err := relayProgram()
	if err != nil {
		return jobProxy{}, err
	}
	server, err := egressproxy.Listen(egressproxy.Options{Destinations: sandbox.Destinations, Log: log})
	if err != nil {
		return jobProxy{}, err
	}
	proxyCtx, cancel := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- server.Serve(proxyCtx) }()
	return jobProxy{server: server, relay: relay, cancel: cancel, served: served, log: log}, nil
}

// relayProgram is the program the sandbox runs as the relay: this one, which answers to
// egressproxy.RelayCommand.
func relayProgram() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("egress proxy: own executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("egress proxy: own executable %s: %w", path, err)
	}
	return resolved, nil
}

// binds are the paths the sandbox of the job binds, the zero value without a proxy.
func (p jobProxy) binds() sandboxProxy {
	if p.server == nil {
		return sandboxProxy{}
	}
	return sandboxProxy{Socket: p.server.SocketPath(), Relay: p.relay}
}

// stop ends the proxy: it closes the open tunnels, waits for them, and removes the socket
// and its directory. The job's log gets one line with the number of refused requests, the
// log lines for single ones being limited.
func (p jobProxy) stop() error {
	if p.server == nil {
		return nil
	}
	p.cancel()
	var err error
	timer := time.NewTimer(proxyStopTimeout)
	defer timer.Stop()
	select {
	case err = <-p.served:
	case <-timer.C:
		err = fmt.Errorf("egress proxy: tunnels still open %s after the job ended", proxyStopTimeout)
	}
	if refused := p.server.Refused(); refused > 0 {
		if _, writeErr := fmt.Fprintf(p.log, "epitrop: egress proxy: %d requests refused\n", refused); writeErr != nil {
			err = errors.Join(err, fmt.Errorf("egress proxy: write the job log: %w", writeErr))
		}
	}
	return errors.Join(err, p.server.Close())
}
