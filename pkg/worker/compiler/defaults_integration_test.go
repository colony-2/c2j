package compiler

import (
	"context"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
)

// TestInjectDefaultsCalledInExecutionPath verifies that InjectDefaults is being called
// correctly during op execution. This test verifies the integration point without
// needing full workflow execution.
func TestInjectDefaultsCalledInExecutionPath(t *testing.T) {
	// Define an op with defaults
	type TestInput struct {
		Name  string `json:"name" default:"default-name"`
		Count int    `json:"count" default:"42"`
	}

	type TestOutput struct {
		Result string `json:"result"`
	}

	// Register the op
	testOp := ops.NewActivityMappedOpV2[TestInput, TestOutput](ops.OpMetadata{
		Type:        "test_defaults",
		Description: "Test op with defaults",
		Version:     "1.0.0",
	}, func(_ ops.OpDependencies, _ context.Context, input TestInput) (TestOutput, error) {
		return TestOutput{Result: "success"}, nil
	})

	ops.Register(testOp)

	// Get the registered op
	registeredOp, exists := ops.Get("test_defaults")
	if !exists {
		t.Fatal("Op not found in registry")
	}

	chain := registeredOp.TaskChain()
	if len(chain) == 0 {
		t.Fatal("Op has no task chain")
	}

	// Simulate what executeOp does: inject defaults into a partial input map
	inputMap := map[string]interface{}{
		"name": "user-provided", // User provides name but not count
	}

	// Call InjectDefaults like executeOp does
	err := workerops.InjectDefaults(chain[0].InputType, inputMap)
	if err != nil {
		t.Fatalf("InjectDefaults failed: %v", err)
	}

	// Verify defaults were injected
	if inputMap["name"] != "user-provided" {
		t.Errorf("Expected name='user-provided', got '%v'", inputMap["name"])
	}

	if inputMap["count"] != "42" {
		t.Errorf("Expected count='42' (default as string), got '%v'", inputMap["count"])
	}
}

// TestInjectDefaultsWithNestedStructs verifies nested struct default handling
func TestInjectDefaultsWithNestedStructs(t *testing.T) {
	type Config struct {
		Host string `json:"host" default:"localhost"`
		Port int    `json:"port" default:"8080"`
	}

	type TestInput struct {
		Name   string `json:"name" default:"test"`
		Config Config `json:"config"`
	}

	type TestOutput struct {
		Result string `json:"result"`
	}

	testOp := ops.NewActivityMappedOpV2[TestInput, TestOutput](ops.OpMetadata{
		Type: "test_nested",
	}, func(_ ops.OpDependencies, _ context.Context, input TestInput) (TestOutput, error) {
		return TestOutput{Result: "success"}, nil
	})

	ops.Register(testOp)

	registeredOp, _ := ops.Get("test_nested")
	chain := registeredOp.TaskChain()

	// Partial nested config
	inputMap := map[string]interface{}{
		"config": map[string]interface{}{
			"host": "prod.example.com",
		},
	}

	err := workerops.InjectDefaults(chain[0].InputType, inputMap)
	if err != nil {
		t.Fatalf("InjectDefaults failed: %v", err)
	}

	// Verify nested defaults
	if inputMap["name"] != "test" {
		t.Errorf("Expected name='test', got '%v'", inputMap["name"])
	}

	configMap, ok := inputMap["config"].(map[string]interface{})
	if !ok {
		t.Fatal("Config is not a map")
	}

	if configMap["host"] != "prod.example.com" {
		t.Errorf("Expected host='prod.example.com', got '%v'", configMap["host"])
	}

	if configMap["port"] != "8080" {
		t.Errorf("Expected port='8080' (default), got '%v'", configMap["port"])
	}
}

// TestInjectDefaultsInputTypeRetrievalTest verifies we can get input types from ops.Get()
func TestInjectDefaultsInputTypeRetrieval(t *testing.T) {
	type TestInput struct {
		Field1 string `json:"field1"`
		Field2 int    `json:"field2"`
	}

	type TestOutput struct {
		Result string `json:"result"`
	}

	testOp := ops.NewActivityMappedOpV2[TestInput, TestOutput](ops.OpMetadata{
		Type: "test_retrieval",
	}, func(_ ops.OpDependencies, _ context.Context, input TestInput) (TestOutput, error) {
		return TestOutput{}, nil
	})

	ops.Register(testOp)

	// Verify we can retrieve the op and its input type
	registeredOp, exists := ops.Get("test_retrieval")
	if !exists {
		t.Fatal("Op not found")
	}

	chain := registeredOp.TaskChain()
	if len(chain) == 0 {
		t.Fatal("Op has no task chain")
	}

	inputType := chain[0].InputType
	if inputType == nil {
		t.Fatal("Input type is nil")
	}

	// Verify it's the correct type
	expectedType := reflect.TypeOf(TestInput{})
	if inputType != expectedType {
		t.Errorf("Expected input type %v, got %v", expectedType, inputType)
	}
}
