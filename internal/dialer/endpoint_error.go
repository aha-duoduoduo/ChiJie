package dialer

import (
	"context"
	"net"
)

// EndpointError identifies failure to connect to the configured proxy itself.
// Target errors and caller mistakes must not change shared proxy health.
type EndpointError struct{ Err error }

func (e *EndpointError) Error() string { return e.Err.Error() }
func (e *EndpointError) Unwrap() error { return e.Err }

type endpointDialer struct{ *net.Dialer }

func (d endpointDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, &EndpointError{Err: err}
	}
	return conn, nil
}
func (d endpointDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}
