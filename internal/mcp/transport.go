package mcp

import "context"

type Transport interface {
	Start(context.Context, func([]byte) error, func(error)) error
	Send(context.Context, []byte) error
	Close(context.Context) error
}
type protocolTransport interface{ SetProtocolVersion(string) }
type requestTransport interface{ EndRequest(int64) }
type notificationsTransport interface{ StartNotifications() }
