package gitcollector

import "fmt"

// GitOperationError represents an error from a git operation
type GitOperationError struct {
	Operation string
	Command   string
	ExitCode  int
	Stderr    string
	Message   string
}

// Error implements the error interface
func (e *GitOperationError) Error() string {
	return fmt.Sprintf("git %s failed: %s", e.Operation, e.Message)
}

// FileCollectionError represents an error during file collection
type FileCollectionError struct {
	Path    string
	Reason  string
	Details map[string]interface{}
}

// Error implements the error interface
func (e *FileCollectionError) Error() string {
	return fmt.Sprintf("failed to collect %s: %s", e.Path, e.Reason)
}
