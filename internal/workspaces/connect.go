package workspaces

import (
	"context"
	"errors"
	"net"
	"sync"
)

type ownedConnection struct {
	net.Conn
	once    sync.Once
	release func()
	err     error
}

func (connection *ownedConnection) Close() error {
	connection.once.Do(func() { defer connection.release(); connection.err = connection.Conn.Close() })
	return connection.err
}
func (manager *Manager) connectWorkspace(ctx context.Context, view *workspaceView, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid port")
	}
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	unlock, err := manager.gates.lock(ctx, workspaceID(view.project, view.thread))
	if err != nil {
		done()
		return nil, err
	}
	lease, err := manager.acquireWorkspace(ctx, view.project, view.thread, view.key)
	unlock()
	if err != nil {
		done()
		return nil, err
	}
	ctx, stop := lease.Context(ctx)
	ctx, stopSource := lease.Value.source.Context(ctx)
	release := func() { stopSource(); stop(); lease.Release(); done() }
	connector, ok := lease.Value.workspace.(interface {
		Connect(context.Context, int) (net.Conn, error)
	})
	if !ok {
		release()
		return nil, errors.New("workspace does not forward ports")
	}
	connection, err := connector.Connect(ctx, port)
	if err != nil {
		release()
		return nil, err
	}
	if connection == nil {
		release()
		return nil, errors.New("workspace returned no connection")
	}
	var mu sync.Mutex
	var stopClose func() bool
	owned := &ownedConnection{Conn: connection}
	owned.release = func() {
		mu.Lock()
		if stopClose != nil {
			stopClose()
		}
		mu.Unlock()
		release()
	}
	mu.Lock()
	stopClose = context.AfterFunc(ctx, func() { owned.Close() })
	mu.Unlock()
	return owned, nil
}
