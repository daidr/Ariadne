package mcp

import (
	"context"
	"fmt"

	"github.com/daidr/Ariadne/internal/process"
)

type StdioOptions struct {
	Argv      []string
	Directory string
	Env       map[string]string
}
type StdioTransport struct{ process *process.JSONLines }

func NewStdio(options StdioOptions) *StdioTransport {
	return &StdioTransport{process: process.NewJSONLines(process.JSONOptions{Argv: options.Argv, Directory: options.Directory, Env: options.Env, ReplaceEnv: true, MaxLineBytes: MaxMessageBytes})}
}
func (transport *StdioTransport) Start(ctx context.Context, take func([]byte) error, end func(error)) error {
	if err := transport.process.Start(ctx, func(data []byte) error {
		if validMessage(data) {
			return take(data)
		}
		return nil
	}, func(err error) { end(&ConnectionError{Reason: err.Error()}) }); err != nil {
		return &ConnectionError{Reason: fmt.Sprint("could not start the MCP server: ", err)}
	}
	return nil
}
func (transport *StdioTransport) Send(ctx context.Context, data []byte) error {
	if len(data) > MaxMessageBytes {
		return &ConnectionError{Reason: "MCP request exceeded its size limit"}
	}
	if err := transport.process.SendJSON(ctx, data); err != nil {
		return &ConnectionError{Reason: err.Error()}
	}
	return nil
}
func (transport *StdioTransport) StderrTail() string { return transport.process.StderrTail() }
func (transport *StdioTransport) Close(ctx context.Context) error {
	return transport.process.Close(ctx)
}
