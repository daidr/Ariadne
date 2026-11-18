package rpc

import (
	"context"
	"errors"

	"github.com/daidr/Ariadne/internal/process"
)

type Stdio struct {
	Peer    *Peer
	Process *process.JSONLines
}

func StartStdio(ctx context.Context, command process.JSONOptions, options Options) (*Stdio, error) {
	lines := process.NewJSONLines(command)
	options.Context = ctx
	options.Send = lines.SendJSON
	peer, err := New(options)
	if err != nil {
		return nil, err
	}
	if err := lines.Start(ctx, peer.Take, peer.End); err != nil {
		peer.End(err)
		_ = lines.Close(context.Background())
		return nil, err
	}
	return &Stdio{Peer: peer, Process: lines}, nil
}
func (stdio *Stdio) Close(ctx context.Context) error {
	stdio.Peer.End(errors.New("agent session closed"))
	if err := stdio.Process.Close(ctx); err != nil {
		return err
	}
	return stdio.Peer.Close(ctx)
}
