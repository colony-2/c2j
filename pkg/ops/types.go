package ops

type OpInputType = any
type OpOutputType = any

type RawMessageOrStruct = any

// InputDefaultsApplier allows ops with dynamic schemas to inject missing input
// values before template resolution and final validation.
type InputDefaultsApplier interface {
	ApplyInputDefaults(input map[string]interface{}) (bool, error)
}
