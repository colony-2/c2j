package ops

import "context"

const (
	MountModeReadOnly  = "ro"
	MountModeReadWrite = "rw"
)

type OperationPaths struct {
	Workdir      string
	WorktreePath string
	Inbox        string
	Outbox       string
}

type OperationPathViews struct {
	Host OperationPaths
	Op   OperationPaths
}

type RequiredMount struct {
	Source string
	Target string
	Mode   string
}

type RequiredPort struct {
	Host string
	Port int
}

type OperationPathRuntime struct {
	Views       OperationPathViews
	Mounts      []RequiredMount
	Ports       []RequiredPort
	SandboxType string
}

type OperationPathTransformRequest struct {
	Input map[string]interface{}
	Host  OperationPaths
}

type OperationPathTransformResult struct {
	Runtime      OperationPathRuntime
	Replacements map[string]string
}

type OperationPathTransformer interface {
	TransformOperationPaths(context.Context, OperationPathTransformRequest) (OperationPathTransformResult, error)
}

type OperationPathProvider interface {
	OperationPaths() OperationPaths
}

type OperationPathRuntimeProvider interface {
	OperationPathRuntime() OperationPathRuntime
}
