package execution

type Mismatch struct {
	Field     string `json:"field"`
	Required  string `json:"required"`
	Allocated string `json:"allocated,omitempty"`
	Reason    string `json:"reason"`
}

// Compare validates both operands. A nil error with no mismatches proves
// compatibility only with this supplied allocation, never lease readiness.
func Compare(required Requirements, allocated Allocation) ([]Mismatch, error) {
	r, err := required.Normalize()
	if err != nil {
		return nil, err
	}
	a, err := allocated.Normalize()
	if err != nil {
		return nil, err
	}
	var result []Mismatch
	supplied := make(map[string]bool, len(a.Capabilities))
	for _, name := range a.Capabilities {
		supplied[name] = true
	}
	for _, name := range r.Capabilities {
		if !supplied[name] {
			result = append(result, Mismatch{Field: "capabilities", Required: name, Reason: "missing_capability"})
		}
	}
	check := func(field string, required *string, actual string, compatible bool) {
		if required == nil || compatible {
			return
		}
		reason := "incompatible"
		if actual == "" {
			reason = "unknown_allocation"
		}
		result = append(result, Mismatch{Field: field, Required: *required, Allocated: actual, Reason: reason})
	}
	if r.Base != nil {
		check("base", r.Base, a.Base, MatchBase(*r.Base, a.Base))
	}
	compareLegacy(r, a, check)

	for _, field := range []struct {
		name             string
		required, actual *string
		cpu              bool
	}{
		{"resources.cpu", r.Resources.CPU, a.Resources.CPU, true},
		{"resources.memory", r.Resources.Memory, a.Resources.Memory, false},
		{"resources.ephemeral-storage", r.Resources.EphemeralStorage, a.Resources.EphemeralStorage, false},
	} {
		if field.required == nil {
			continue
		}
		actual, compatible := "", false
		if field.actual != nil {
			actual = *field.actual
			_, minimum, _ := parseQuantity(*field.required, field.cpu)
			_, capacity, _ := parseQuantity(actual, field.cpu)
			compatible = capacity >= minimum
		}
		check(field.name, field.required, actual, compatible)
	}
	return result, nil
}
