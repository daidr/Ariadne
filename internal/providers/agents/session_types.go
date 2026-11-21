package agents

import (
	"context"
	"encoding/json"

	"github.com/daidr/Ariadne/sdk"
)

type ToolOutput struct {
	Content string      `json:"content"`
	IsError bool        `json:"isError"`
	Images  []sdk.Image `json:"images"`
}
type Usage struct {
	Input, Output                               int64
	ContextInput, Cached, CacheWrite, Reasoning *int64
	CostUSD                                     *float64
}
type Event struct {
	Type, Text, ID, Name, Arguments, Detail string
	Usage                                   Usage
	StopReason                              string
	Retryable, Busy                         bool
	AgentError                              *sdk.AgentError
}
type AgentTools struct {
	Specs []sdk.Tool
	Call  func(context.Context, string, any) ToolOutput
}
type DriverContext struct {
	Settings   map[string]any
	Model      string
	Effort     string
	System     string
	Tools      AgentTools
	Directory  string
	Persistent bool
	Resume     json.RawMessage
	Emit       func(Event)
}

// Drivers use the lifetime passed to Start for their process, and the invocation
// context for individual operations. Close must release pending RPC calls.
type Driver interface {
	Start(context.Context) error
	Prompt(context.Context, Input, string) error
	Close(context.Context, bool) error
}
type HistorySeeder interface {
	Seed(context.Context, []sdk.Message) (Input, error)
}
type HistoryBudgeter interface {
	HistoryBudget(system string) int
}
type EffortConfigurer interface {
	Configure(context.Context, string) error
}
type Checkpointer interface {
	Checkpoint() (json.RawMessage, error)
}
type Kind struct {
	Name            string
	Persistent      bool
	ResumeDirectory bool
	MutableEffort   bool
	Fingerprint     func(sdk.Message) string
	Create          func(DriverContext) (Driver, error)
}

const PlaceNote = "You run through a helper program on the user's own computer. Anything it says about its working directory, shell, operating system or sandbox describes that program, not your workspace: the workspace is wherever your tools run, as described above, and it may be another machine. Read, change and run things only with the tools you were given."
