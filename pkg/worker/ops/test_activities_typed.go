package ops

import (
	"context"
	"fmt"

	recipeops "github.com/colony-2/c2j/pkg/ops"
)

func registerTypedTestActivities() {
	type testComplexInput struct {
		Config map[string]interface{} `json:"config"`
		Meta   struct {
			Label string   `json:"label"`
			Flags []string `json:"flags"`
		} `json:"meta"`
	}

	type testComplexOutput struct {
		Config map[string]interface{} `json:"config"`
		Meta   struct {
			Label string   `json:"label"`
			Flags []string `json:"flags"`
		} `json:"meta"`
	}

	testComplex := recipeops.NewActivityMappedOpV2[testComplexInput, testComplexOutput](
		recipeops.OpMetadata{
			Type: "test_complex_input",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input testComplexInput) (testComplexOutput, error) {
			return testComplexOutput{
				Config: input.Config,
				Meta:   input.Meta,
			}, nil
		},
	)
	recipeops.Register(testComplex)

	// Register context_logger activity
	contextLogger := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "context_logger",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			return GenericOutput{
				Logged: true,
			}, nil
		},
	)
	recipeops.Register(contextLogger)

	// Register batch_validator activity
	batchValidator := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "batch_validator",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			if input.Items == nil {
				return GenericOutput{}, fmt.Errorf("items array is required")
			}
			return GenericOutput{
				Valid: true,
				Count: len(input.Items),
				Items: input.Items, // Return the validated items
			}, nil
		},
	)
	recipeops.Register(batchValidator)

	// Register gemini_report_activity
	geminiReport := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "gemini_report_activity",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			return GenericOutput{
				Report: "Generated report",
				Status: "complete",
			}, nil
		},
	)
	recipeops.Register(geminiReport)

	// Register llm activity (for tests that reference it)
	llmActivity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "llm",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			prompt := input.Message
			if prompt == "" && input.Extra != nil {
				if p, ok := input.Extra["prompt"].(string); ok {
					prompt = p
				}
			}
			return GenericOutput{
				Response: fmt.Sprintf("LLM response to: %s", prompt),
				Model:    input.Extra["model"],
			}, nil
		},
	)
	recipeops.Register(llmActivity)

	// Register http activity (for tests that reference it)
	httpActivity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "http",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			return GenericOutput{
				Status: "200",
				Body:   "HTTP response",
			}, nil
		},
	)
	recipeops.Register(httpActivity)

	// Register data transformation activities
	dataActivities := []string{
		"process-data",
		"prepare-data",
		"transform-data",
		"validate-data",
		"enrich-data",
		"fetch-data",
		"aggregate-data",
		"clean-data",
		"data-processor",
		"data-validator",
		"data-enricher",
	}

	for _, activityName := range dataActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				output := GenericOutput{
					Status: "processed",
				}
				// Pass through data if provided
				if input.Data != nil {
					output.Data = input.Data
				}
				return output, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register test activities that were previously registered in tests
	testActivities := []string{
		"test_activity",
		"test-activity",
		"http-activity",
		"grpc-activity",
		"simple-activity",
		"complex-activity",
		"error-activity",
	}

	for _, activityName := range testActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				// Check if we should simulate an error
				if name == "error-activity" && input.Error {
					return GenericOutput{}, fmt.Errorf("simulated error")
				}
				return GenericOutput{
					Result: fmt.Sprintf("Executed %s", name),
					Status: "success",
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register workflow control activities
	workflowActivities := []string{
		"start-workflow",
		"check-status",
		"cancel-workflow",
		"signal-workflow",
		"query-workflow",
	}

	for _, activityName := range workflowActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Status: "success",
					Action: name,
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register ML/Analytics activities
	mlActivities := []string{
		"active_learning_sampler",
		"advanced_ml_analyzer",
		"basic_ml_analyzer",
		"ml_classifier",
		"feature_extractor",
		"topic_modeler",
		"sentiment_analyzer",
		"entity_extractor",
		"entity_linker",
		"pattern_detector",
		"statistical_analyzer",
		"auto_classifier",
		"language_detector",
		"english_extractor",
		"spanish_extractor",
		"universal_extractor",
		"field_extractor",
		"metadata_extractor",
	}

	for _, activityName := range mlActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Result:     fmt.Sprintf("ML analysis from %s", name),
					Status:     "analyzed",
					Confidence: 0.95,
					Model:      name,
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register data processing activities
	processingActivities := []string{
		"batch_processor",
		"parallel_processor",
		"data_aggregator",
		"data_analyst",
		"data_cleaner",
		"data_normalizer",
		"data_transformer",
		"data_validator",
		"dataset_validator",
		"metadata_enricher",
		"analyze_data",
		"analyze_op",
		"process_data",
		"prepare_data",
		"transformData",
		"validateData",
		"fetchExternalData",
		"fetchExternalDataSlowPath",
		"persistData",
		"createDefaultData",
		"attemptDataRepair",
		"fallbackTransform",
		"scheduleDataReconciliation",
		"unified_data_pipeline",
		"unified-data-pipeline",
		"data-aggregation-pattern",
		"data-analyzer",
		"data-processor",
		"data-validator",
		"aggregation",
		"analysis_validator",
		"validation",
		"validation_activity",
		"validate_sources",
		"quality_filter",
		"quality_check_activity",
		"quality_review_machine",
		"queue_for_review",
		"send_to_review_queue",
		"enrich_activity",
		"transform_activity",
	}

	for _, activityName := range processingActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Result: fmt.Sprintf("Processed by %s", name),
					Status: "processed",
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register report/output activities
	reportActivities := []string{
		"report_builder",
		"report_generator",
		"report_writer",
		"write_report_op",
		"summary_generator",
		"create_summary",
		"summarize_activity",
		"chart_generator",
		"table_generator",
		"document_loader",
		"output_formatter",
		"json_formatter",
		"api_formatter",
		"format_text",
		"combine_results",
		"result_aggregator",
		"result_storage",
		"result_schema_validator",
	}

	for _, activityName := range reportActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Report: fmt.Sprintf("Report from %s", name),
					Status: "generated",
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register error/retry activities
	errorActivities := []string{
		"error_handler",
		"error-handling",
		"exponentialBackoff",
		"calculateBackoff",
		"waitForRateLimit",
		"refreshAuthToken",
		"updateCredentials",
		"triggerAlert",
		"captureErrorMetrics",
		"logGenericError",
		"logNotFound",
		"logRateLimit",
		"logSaveError",
		"logTimeout",
		"notifyAuthFailure",
		"notifyCriticalFailure",
		"notifyPartialSuccess",
		"notifyProcessingError",
		"notifyRateLimitExceeded",
		"notifySuccess",
		"notifyValidationFailure",
		"publishSuccess",
		"saveToBackupStorage",
		"audit_logger",
	}

	for _, activityName := range errorActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				// Simulate errors for certain activities
				if input.Error {
					return GenericOutput{}, fmt.Errorf("simulated error in %s", name)
				}
				return GenericOutput{
					Status:  "handled",
					Handler: name,
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register processor activities
	processorActivities := []string{
		"high_confidence_processor",
		"medium_confidence_processor",
		"standard_processor",
		"fast_processor",
		"standard_analyzer",
		"fast_analyzer",
		"enhanced_analyzer",
		"business_rule_engine",
		"schema_validator",
	}

	for _, activityName := range processorActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Result:    fmt.Sprintf("Processed by %s", name),
					Status:    "success",
					Processor: name,
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register search/fetch activities
	searchActivities := []string{
		"quick_search_activity",
		"fetch_dataset_activity",
		"research_op",
		"user_input_form",
	}

	for _, activityName := range searchActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Result: fmt.Sprintf("Found by %s", name),
					Status: "found",
				}, nil
			},
		)
		recipeops.Register(activity)
	}

	// Register recipe invocation activity
	recipeActivity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "recipe",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			// For test purposes, simulate recipe invocation
			recipeName := ""
			if input.Extra != nil {
				if cfg, ok := input.Extra["config"].(map[string]interface{}); ok {
					if name, ok := cfg["recipe"].(string); ok {
						recipeName = name
					}
				}
			}
			return GenericOutput{
				Result: map[string]interface{}{
					"sentiment_score": 0.8,
					"emotions":        []string{"positive"},
					"topics":          []string{"topic1", "topic2"},
					"keywords":        []string{"key1", "key2"},
					"entities":        []string{"entity1"},
					"relationships":   []string{"rel1"},
					"language":        "en",
					"confidence":      0.95,
					"insights":        []string{"insight1"},
					"recommendations": []string{"rec1"},
					"data":            map[string]interface{}{"processed": true},
					"metrics":         map[string]interface{}{"count": 100},
					"quality_score":   0.85,
				},
				Status: "success",
				Recipe: recipeName,
			}, nil
		},
	)
	recipeops.Register(recipeActivity)

	// Register test/example activities
	miscActivities := []string{
		"activity",
		"basic-test",
		"context-propagation",
		"echo-example",
		"finalize_basic",
		"invocation",
		"minimal_workflow",
		"nested-composition",
		"parallel-child-invocation",
		"parallel-recipe",
		"parent-child-basic",
		"simple_workflow",
		"simple-echo",
		"simple-recipe",
		"simple-state-machine",
		"state_machine",
		"state-machine-composition",
		"template-features",
		"test-command-execution",
		"test-execute",
		"test-invalid",
		"test-missing-fields",
		"test-parallel-node",
		"test-sequence-node",
		"test-sleep-operation",
		"test-state-machine",
		"wait",
		"gemini-recipe",
		"dataset-processor",
		"sentiment-analyzer",
		"topic-extractor",
		"entity-recognizer",
		"language-detector",
		"insight-generator",
	}

	for _, activityName := range miscActivities {
		name := activityName // capture for closure
		activity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
			recipeops.OpMetadata{
				Type: name,
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
				return GenericOutput{
					Result: fmt.Sprintf("Executed %s", name),
					Status: "success",
				}, nil
			},
		)
		recipeops.Register(activity)
	}
}
