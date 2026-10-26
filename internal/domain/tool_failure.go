package domain

// ToolError contains only classified, model-safe diagnostics. Never put raw
// backend messages, caller values or credentials in these fields.
type ToolError struct {
	Code      string   `json:"error"`
	Recovery  string   `json:"recovery"`
	Module    string   `json:"module,omitempty"`
	Method    string   `json:"method,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	Exception string   `json:"exception,omitempty"`
}

func (e *ToolError) Error() string { return e.Code }
