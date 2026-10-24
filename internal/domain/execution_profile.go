package domain

import "net/http"

// ExecutionProfile can only restrict the authority available to a run.
type ExecutionProfile string

const ProfileNativeFiles ExecutionProfile = "native_files"

func (p ExecutionProfile) Validate() error {
	if p != "" && p != ProfileNativeFiles {
		return NewError(http.StatusUnprocessableEntity, "invalid_execution_profile")
	}
	return nil
}

func (r Run) NativeFilesOnly() bool { return r.ExecutionProfile == ProfileNativeFiles }

func (r Run) AllowsModelTool(name string) bool {
	if !r.NativeFilesOnly() {
		return true
	}
	switch name {
	case "script", "artifact_attach", "question":
		return true
	default:
		return false
	}
}
