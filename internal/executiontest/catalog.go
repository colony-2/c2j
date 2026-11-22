// Package executiontest supplies complete, deliberately permissive declarations
// for tests of admission boundaries. Tests of limits set their rules explicitly.
package executiontest

import "github.com/colony-2/c2j/pkg/execution"

func Offerings(names ...string) []execution.BaseOffering {
	out := []execution.BaseOffering{}
	for _, name := range names {
		out = append(out, execution.BaseOffering{Base: name, Name: "default", Capabilities: []string{}, Resources: &execution.OfferingResources{CPU: &execution.ResourceRule{State: "unbounded"}, Memory: &execution.ResourceRule{State: "unbounded"}}})
	}
	return out
}
