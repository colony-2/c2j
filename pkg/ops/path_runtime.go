package ops

// OperationPaths are paths visible to operations in the worker environment.
type OperationPaths struct {
	Workdir      string
	WorktreePath string
	Inbox        string
	Outbox       string
}

type OperationPathProvider interface{ OperationPaths() OperationPaths }
