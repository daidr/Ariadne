package sdk

type LocalizedText map[string]string
type SettingField struct {
	Type          string        `json:"type"`
	Title         LocalizedText `json:"title"`
	Description   LocalizedText `json:"description,omitempty"`
	Secret        bool          `json:"secret,omitempty"`
	Directory     bool          `json:"directory,omitempty"`
	NetworkOrigin bool          `json:"networkOrigin,omitempty"`
	Default       any           `json:"default,omitempty"`
	Options       []string      `json:"options,omitempty"`
}
type SettingsSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]SettingField `json:"properties"`
	Required   []string                `json:"required,omitempty"`
	// FieldOrder keeps forms in the provider's declared order across JSON/RPC.
	FieldOrder []string `json:"-"`
}
type ReasoningScope string

const (
	// ReasoningScopeModel replays reasoning only for the same provider, model and settings.
	ReasoningScopeModel ReasoningScope = "model"
	// ReasoningScopeProvider replays reasoning across models sharing the same provider and settings.
	ReasoningScopeProvider ReasoningScope = "provider"
)

type ModelProviderCapabilities struct {
	ReasoningScope     ReasoningScope `json:"reasoningScope,omitempty"`
	StatefulSessions   bool           `json:"statefulSessions,omitempty"`
	PreserveModelOrder bool           `json:"preserveModelOrder,omitempty"`
	PreferDefaultModel bool           `json:"preferDefaultModel,omitempty"`
}

type ModelProviderInfo struct {
	Kind         string                    `json:"kind"`
	Title        LocalizedText             `json:"title"`
	Settings     SettingsSchema            `json:"settings"`
	Program      bool                      `json:"program,omitempty"`
	Capabilities ModelProviderCapabilities `json:"capabilities,omitempty"`
}

// HasStatefulSessions reports whether the provider maintains cross-turn session state
// (e.g. a local agent CLI subprocess) rather than stateless prompt replay.
func (info ModelProviderInfo) HasStatefulSessions() bool {
	return info.Capabilities.StatefulSessions || info.Program
}

// PreservesModelOrder reports whether model listing order from the provider should be kept as-is.
func (info ModelProviderInfo) PreservesModelOrder() bool {
	return info.Capabilities.PreserveModelOrder || info.Program
}

// PrefersDefaultModel reports whether auto-discovered setups should pick "default" when offered.
func (info ModelProviderInfo) PrefersDefaultModel() bool {
	return info.Capabilities.PreferDefaultModel || info.Program
}

var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

func EffortWithin(wanted string, accepted *[]string) string {
	values := []string{"low", "medium", "high"}
	if accepted != nil {
		values = *accepted
	}
	contains := func(value string) bool {
		for _, item := range values {
			if item == value {
				return true
			}
		}
		return false
	}
	if contains(wanted) {
		return wanted
	}
	rank := -1
	for i, value := range Efforts {
		if value == wanted {
			rank = i
			break
		}
	}
	first, before := "", ""
	for index, value := range Efforts {
		if contains(value) {
			if first == "" {
				first = value
			}
			if index < rank {
				before = value
			}
		}
	}
	if before != "" {
		return before
	}
	return first
}
