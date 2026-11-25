package uptime

import (
	"context"
	"net"
)

func probeTCP(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}
