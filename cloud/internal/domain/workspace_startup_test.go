package domain

import "testing"

// A worker reports an occupied working folder with its own startup error
// code, which the control plane accepts like the other workspace codes.
func TestWorkspaceStartupErrorCodesIncludeOccupiedWorkingFolder(t *testing.T) {
	t.Parallel()
	if !IsWorkspaceStartupErrorCode(WorkspaceStartupErrorWorkingDirectoryOccupied) {
		t.Fatal("working_directory_occupied is not a workspace startup error code")
	}
}
