package recipe

// ChildGroupData describes a first-class recipe node that fans out to child
// recipes. The compiler lowers this node to internal task-backed recipe ops.
type ChildGroupData struct {
	Mode         string              `yaml:"mode,omitempty" json:"mode,omitempty"`
	Children     []ChildGroupChild   `yaml:"children,omitempty" json:"children,omitempty"`
	ChildrenFrom interface{}         `yaml:"children_from,omitempty" json:"children_from,omitempty"`
	Child        *ChildGroupChild    `yaml:"child,omitempty" json:"child,omitempty"`
	Artifacts    ChildGroupArtifacts `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Aggregate    ChildGroupAggregate `yaml:"aggregate,omitempty" json:"aggregate,omitempty"`
}

type ChildGroupArtifacts struct {
	Use []interface{} `yaml:"use,omitempty" json:"use,omitempty"`
}

type ChildGroupAggregate struct {
	Shape    interface{} `yaml:"shape,omitempty" json:"shape,omitempty"`
	Artifact interface{} `yaml:"artifact,omitempty" json:"artifact,omitempty"`
}

type ChildGroupChild struct {
	Key        interface{}            `yaml:"key,omitempty" json:"key,omitempty"`
	Recipe     interface{}            `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	CellName   interface{}            `yaml:"cell_name,omitempty" json:"cell_name,omitempty"`
	Required   interface{}            `yaml:"required,omitempty" json:"required,omitempty"`
	When       interface{}            `yaml:"when,omitempty" json:"when,omitempty"`
	SkipReason interface{}            `yaml:"skip_reason,omitempty" json:"skip_reason,omitempty"`
	GitRef     interface{}            `yaml:"git_ref,omitempty" json:"git_ref,omitempty"`
	Inputs     map[string]interface{} `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Artifacts  []interface{}          `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
}

type RecipeChildGroup struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	ChildGroup     ChildGroupData `yaml:"child_group" json:"child_group"`
}

func (r *RecipeChildGroup) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeChildGroup) isRecipe() {}

type NodeChildGroup struct {
	NodeMetadata `yaml:",inline"`
	ChildGroup   ChildGroupData `yaml:"child_group" json:"child_group"`
}

func (n *NodeChildGroup) GetMetadata() NodeMetadata {
	return n.NodeMetadata
}

func (n *NodeChildGroup) isNode() {}
