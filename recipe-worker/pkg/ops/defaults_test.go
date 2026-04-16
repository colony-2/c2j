package ops

import (
	"reflect"
	"testing"
)

func TestInjectDefaults_TopLevel(t *testing.T) {
	type TestInput struct {
		Name  string `json:"name" default:"test"`
		Count int    `json:"count" default:"5"`
	}

	inputMap := map[string]interface{}{
		"name": "override",
	}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inputMap["name"] != "override" {
		t.Errorf("expected name to be 'override', got %v", inputMap["name"])
	}

	if inputMap["count"] != "5" {
		t.Errorf("expected count to be '5', got %v", inputMap["count"])
	}
}

func TestInjectDefaults_Nested(t *testing.T) {
	type DatabaseConfig struct {
		Host string `json:"host" default:"localhost"`
		Port int    `json:"port" default:"5432"`
	}

	type TestInput struct {
		Name   string         `json:"name" default:"test"`
		Config DatabaseConfig `json:"config"`
	}

	inputMap := map[string]interface{}{
		"config": map[string]interface{}{
			"host": "prod.example.com",
		},
	}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level default injected
	if inputMap["name"] != "test" {
		t.Errorf("expected name to be 'test', got %v", inputMap["name"])
	}

	// Nested structure preserved
	configMap, ok := inputMap["config"].(map[string]interface{})
	if !ok {
		t.Fatal("expected config to be a map")
	}

	if configMap["host"] != "prod.example.com" {
		t.Errorf("expected host to be 'prod.example.com', got %v", configMap["host"])
	}

	if configMap["port"] != "5432" {
		t.Errorf("expected port to be '5432', got %v", configMap["port"])
	}
}

func TestInjectDefaults_TemplateExpression(t *testing.T) {
	type TestInput struct {
		Branch string `json:"branch" default:"{{ context.git.branch }}"`
		Port   int    `json:"port" default:"${{ inputs.base_port + 1000 }}"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Template strings injected (will be resolved later)
	if inputMap["branch"] != "{{ context.git.branch }}" {
		t.Errorf("expected branch template, got %v", inputMap["branch"])
	}

	if inputMap["port"] != "${{ inputs.base_port + 1000 }}" {
		t.Errorf("expected port template, got %v", inputMap["port"])
	}
}

func TestInjectDefaults_EmptyNestedStruct(t *testing.T) {
	type Config struct {
		Setting string `json:"setting" default:"value"`
	}

	type TestInput struct {
		Config Config `json:"config"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nested map created with default
	configMap, ok := inputMap["config"].(map[string]interface{})
	if !ok {
		t.Fatal("expected config to be created")
	}

	if configMap["setting"] != "value" {
		t.Errorf("expected setting to be 'value', got %v", configMap["setting"])
	}
}

func TestInjectDefaults_NoDefaults(t *testing.T) {
	type Config struct {
		Setting string `json:"setting"` // no default
	}

	type TestInput struct {
		Config Config `json:"config"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No nested map created since no defaults exist
	_, exists := inputMap["config"]
	if exists {
		t.Error("expected config to not exist when no defaults present")
	}
}

func TestInjectDefaults_PointerFields(t *testing.T) {
	type Config struct {
		Setting string `json:"setting" default:"value"`
	}

	type TestInput struct {
		Config *Config `json:"config"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nested map created for pointer struct
	configMap, ok := inputMap["config"].(map[string]interface{})
	if !ok {
		t.Fatal("expected config to be created for pointer type")
	}

	if configMap["setting"] != "value" {
		t.Errorf("expected setting to be 'value', got %v", configMap["setting"])
	}
}

func TestInjectDefaults_UserProvidedNonMap(t *testing.T) {
	type Config struct {
		Setting string `json:"setting" default:"value"`
	}

	type TestInput struct {
		Config Config `json:"config"`
	}

	inputMap := map[string]interface{}{
		"config": "some-string", // wrong type
	}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// User's invalid value preserved, no defaults injected
	if inputMap["config"] != "some-string" {
		t.Errorf("expected config to be preserved as 'some-string', got %v", inputMap["config"])
	}
}

func TestInjectDefaults_DeepNesting(t *testing.T) {
	type Level3 struct {
		Value string `json:"value" default:"deep"`
	}

	type Level2 struct {
		Level3 Level3 `json:"level3"`
		Name   string `json:"name" default:"middle"`
	}

	type Level1 struct {
		Level2 Level2 `json:"level2"`
		Count  int    `json:"count" default:"10"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(Level1{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check top level
	if inputMap["count"] != "10" {
		t.Errorf("expected count to be '10', got %v", inputMap["count"])
	}

	// Check level 2
	level2, ok := inputMap["level2"].(map[string]interface{})
	if !ok {
		t.Fatal("expected level2 to be created")
	}

	if level2["name"] != "middle" {
		t.Errorf("expected name to be 'middle', got %v", level2["name"])
	}

	// Check level 3
	level3, ok := level2["level3"].(map[string]interface{})
	if !ok {
		t.Fatal("expected level3 to be created")
	}

	if level3["value"] != "deep" {
		t.Errorf("expected value to be 'deep', got %v", level3["value"])
	}
}

func TestInjectDefaults_PartialNesting(t *testing.T) {
	type Inner struct {
		Field1 string `json:"field1" default:"default1"`
		Field2 string `json:"field2" default:"default2"`
	}

	type Outer struct {
		Inner Inner  `json:"inner"`
		Name  string `json:"name" default:"outer"`
	}

	inputMap := map[string]interface{}{
		"inner": map[string]interface{}{
			"field1": "provided",
		},
	}

	err := InjectDefaults(reflect.TypeOf(Outer{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top level default
	if inputMap["name"] != "outer" {
		t.Errorf("expected name to be 'outer', got %v", inputMap["name"])
	}

	inner, ok := inputMap["inner"].(map[string]interface{})
	if !ok {
		t.Fatal("expected inner to be a map")
	}

	// User provided field1
	if inner["field1"] != "provided" {
		t.Errorf("expected field1 to be 'provided', got %v", inner["field1"])
	}

	// Default for field2
	if inner["field2"] != "default2" {
		t.Errorf("expected field2 to be 'default2', got %v", inner["field2"])
	}
}

func TestInjectDefaults_OmitEmpty(t *testing.T) {
	type TestInput struct {
		Required string `json:"required" default:"value"`
		Optional string `json:"optional,omitempty" default:"optval"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inputMap["required"] != "value" {
		t.Errorf("expected required to be 'value', got %v", inputMap["required"])
	}

	if inputMap["optional"] != "optval" {
		t.Errorf("expected optional to be 'optval', got %v", inputMap["optional"])
	}
}

func TestInjectDefaults_NonStructType(t *testing.T) {
	inputMap := map[string]interface{}{}

	// Should handle gracefully
	err := InjectDefaults(reflect.TypeOf("string"), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(inputMap) != 0 {
		t.Error("expected input map to remain empty")
	}
}

func TestInjectDefaults_PtrToStruct(t *testing.T) {
	type TestInput struct {
		Name string `json:"name" default:"test"`
	}

	inputMap := map[string]interface{}{}

	// Pass pointer type
	err := InjectDefaults(reflect.TypeOf(&TestInput{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inputMap["name"] != "test" {
		t.Errorf("expected name to be 'test', got %v", inputMap["name"])
	}
}

// Regression coverage for recipe-child SingleRecipeWithRef input shapes.
// The embedded struct uses json:",squash", and its defaults should propagate.
func TestInjectDefaults_SquashedEmbeddedRecipe(t *testing.T) {
	type SingleRecipeGit struct {
		BaseRef string `json:"base_ref" default:"{{ context.git.base_ref }}"`
	}

	type SingleRecipe struct {
		Name     string          `json:"name" default:"demo-recipe"`
		CellPath string          `json:"cell_path" default:"{{ context.workflow.cell_path }}"`
		Git      SingleRecipeGit `json:"git"`
	}

	type SingleRecipeWithRef struct {
		SingleRecipe `json:",squash"`
		GitRef       string `json:"git_ref" default:"{{ context.git.ref }}"`
	}

	inputMap := map[string]interface{}{}

	err := InjectDefaults(reflect.TypeOf(SingleRecipeWithRef{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level default from the wrapper is injected.
	if inputMap["git_ref"] != "{{ context.git.ref }}" {
		t.Errorf("expected git_ref default to be injected, got %v", inputMap["git_ref"])
	}

	// Defaults from the squashed embedded struct are currently not injected.
	if _, ok := inputMap["name"]; !ok {
		t.Errorf("expected name default from embedded struct to be injected, but it is missing")
	}

	if _, ok := inputMap["cell_path"]; !ok {
		t.Errorf("expected cell_path default from embedded struct to be injected, but it is missing")
	}

	gitValue, ok := inputMap["git"]
	if !ok {
		t.Errorf("expected git defaults to be injected, but git map is missing")
	} else if gitMap, ok := gitValue.(map[string]interface{}); ok {
		if gitMap["base_ref"] != "{{ context.git.base_ref }}" {
			t.Errorf("expected base_ref default to be injected, got %v", gitMap["base_ref"])
		}
	}
}

// Regression coverage for recipe-child MultipleRecipes input shapes.
// Defaults inside slice elements should be injected for each recipe entry.
func TestInjectDefaults_SliceOfRecipes(t *testing.T) {
	type Recipe struct {
		CellName string `json:"cell_name" default:"{{ context.workflow.cell }}"`
		Git      struct {
			BaseRef string `json:"base_ref" default:"{{ context.git.base_ref }}"`
		} `json:"git"`
	}

	type MultipleRecipes struct {
		GitRef  string   `json:"git_ref" default:"{{ context.git.ref }}"`
		Recipes []Recipe `json:"recipes"`
	}

	inputMap := map[string]interface{}{
		"recipes": []interface{}{
			map[string]interface{}{},
		},
	}

	err := InjectDefaults(reflect.TypeOf(MultipleRecipes{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inputMap["git_ref"] != "{{ context.git.ref }}" {
		t.Errorf("expected git_ref default to be injected, got %v", inputMap["git_ref"])
	}

	recipes, ok := inputMap["recipes"].([]interface{})
	if !ok || len(recipes) == 0 {
		t.Fatalf("expected recipes slice to be present with one entry, got %v", inputMap["recipes"])
	}

	firstRecipe, ok := recipes[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected first recipe to be a map, got %T", recipes[0])
	}

	if _, ok := firstRecipe["cell_name"]; !ok {
		t.Errorf("expected cell_name default to be injected into recipe entry, but it is missing")
	}

	git, ok := firstRecipe["git"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected git map to be injected into recipe entry, got %T", firstRecipe["git"])
	}

	if git["base_ref"] != "{{ context.git.base_ref }}" {
		t.Errorf("expected git.base_ref default to be injected, got %v", git["base_ref"])
	}
}

// Ensure defaults are injected when recipes slice is typed as []map[string]interface{}.
func TestInjectDefaults_SliceOfMapsRecipes(t *testing.T) {
	type Recipe struct {
		CellPath string `json:"cell_path" default:"{{ context.workflow.cell_path }}"`
	}

	type MultipleRecipes struct {
		GitRef  string   `json:"git_ref" default:"{{ context.git.ref }}"`
		Recipes []Recipe `json:"recipes"`
	}

	inputMap := map[string]interface{}{
		"recipes": []map[string]interface{}{
			{},
		},
	}

	err := InjectDefaults(reflect.TypeOf(MultipleRecipes{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	recipesTyped, ok := inputMap["recipes"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected recipes to remain []map[string]interface{}, got %T", inputMap["recipes"])
	}

	if len(recipesTyped) != 1 {
		t.Fatalf("expected one recipe, got %d", len(recipesTyped))
	}

	if recipesTyped[0]["cell_path"] != "{{ context.workflow.cell_path }}" {
		t.Errorf("expected cell_path default injected, got %v", recipesTyped[0]["cell_path"])
	}
}

// Embedded defaults inside slice elements (json:",squash") should be inlined.
func TestInjectDefaults_SliceElementsWithSquash(t *testing.T) {
	type Embedded struct {
		Env string `json:"env" default:"dev"`
	}

	type Item struct {
		Embedded `json:",squash"`
		Name     string `json:"name" default:"item"`
	}

	type Wrapper struct {
		Items []Item `json:"items"`
	}

	inputMap := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{},
		},
	}

	err := InjectDefaults(reflect.TypeOf(Wrapper{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	items, ok := inputMap["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected one item, got %v", inputMap["items"])
	}

	itemMap, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected item to be a map, got %T", items[0])
	}

	if itemMap["env"] != "dev" {
		t.Errorf("expected embedded env default 'dev', got %v", itemMap["env"])
	}
	if itemMap["name"] != "item" {
		t.Errorf("expected name default 'item', got %v", itemMap["name"])
	}
}

// Full shape copied from recipe-child/pkg/recipe/op.go to mirror production inputs.
func TestInjectDefaults_MultipleRecipes_ExactShape(t *testing.T) {
	type SingleRecipeGit struct {
		BaseRepo string `json:"base_repo,omitempty" default:"{{ context.git.repo }}"`
		BaseRef  string `json:"base_ref,omitempty" default:"{{ context.git.base_ref }}"`
		BaseHash string `json:"base_hash,omitempty" default:"{{ context.git.base_hash }}"`
		Author   string `json:"author,omitempty" default:"{{ context.git.author }}"`
	}

	type SingleRecipe struct {
		Name      string                 `json:"name" validate:"required"`
		CellName  string                 `json:"cell_name,omitempty" default:"{{ context.workflow.cell }}"`
		CellPath  string                 `json:"cell_path,omitempty" default:"{{ context.workflow.cell_path }}"`
		Inputs    map[string]interface{} `json:"inputs"`
		Artifacts []interface{}          `json:"artifacts"` // swf.ArtifactKey omitted for brevity
		Git       SingleRecipeGit        `json:"git"`
	}

	type MultipleRecipes struct {
		GitRef  string         `json:"git_ref" default:"{{ context.git.ref }}" validate:"required"`
		Recipes []SingleRecipe `json:"recipes"`
	}

	inputMap := map[string]interface{}{
		// Two recipes to ensure each element is processed
		"recipes": []interface{}{
			map[string]interface{}{}, // all defaults
			map[string]interface{}{
				"name": "explicit-name", // should be preserved
				"git": map[string]interface{}{
					"author": "provided-author", // should be preserved
				},
			},
		},
	}

	err := InjectDefaults(reflect.TypeOf(MultipleRecipes{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level default
	if inputMap["git_ref"] != "{{ context.git.ref }}" {
		t.Errorf("expected git_ref default to be injected, got %v", inputMap["git_ref"])
	}

	recipes, ok := inputMap["recipes"].([]interface{})
	if !ok || len(recipes) != 2 {
		t.Fatalf("expected two recipes slice, got %v", inputMap["recipes"])
	}

	// First recipe: all defaults
	first := recipes[0].(map[string]interface{})
	if first["cell_name"] != "{{ context.workflow.cell }}" {
		t.Errorf("expected cell_name default, got %v", first["cell_name"])
	}
	if first["cell_path"] != "{{ context.workflow.cell_path }}" {
		t.Errorf("expected cell_path default, got %v", first["cell_path"])
	}
	git0 := first["git"].(map[string]interface{})
	if git0["base_repo"] != "{{ context.git.repo }}" ||
		git0["base_ref"] != "{{ context.git.base_ref }}" ||
		git0["base_hash"] != "{{ context.git.base_hash }}" ||
		git0["author"] != "{{ context.git.author }}" {
		t.Errorf("expected git defaults injected for first recipe, got %v", git0)
	}

	// Second recipe: preserves provided values, injects missing ones
	second := recipes[1].(map[string]interface{})
	if second["name"] != "explicit-name" {
		t.Errorf("expected provided name to remain, got %v", second["name"])
	}
	if second["cell_name"] != "{{ context.workflow.cell }}" {
		t.Errorf("expected cell_name default on second recipe, got %v", second["cell_name"])
	}
	if second["cell_path"] != "{{ context.workflow.cell_path }}" {
		t.Errorf("expected cell_path default on second recipe, got %v", second["cell_path"])
	}
	git1 := second["git"].(map[string]interface{})
	if git1["author"] != "provided-author" {
		t.Errorf("expected provided author to remain, got %v", git1["author"])
	}
	if git1["base_repo"] != "{{ context.git.repo }}" ||
		git1["base_ref"] != "{{ context.git.base_ref }}" ||
		git1["base_hash"] != "{{ context.git.base_hash }}" {
		t.Errorf("expected missing git defaults injected on second recipe, got %v", git1)
	}
}

// Mirror live payload: artifacts slice present, inputs map present, defaults missing.
func TestInjectDefaults_MultipleRecipes_WithArtifactsAndInputs(t *testing.T) {
	type SingleRecipeGit struct {
		BaseRepo string `json:"base_repo,omitempty" default:"{{ context.git.repo }}"`
		BaseRef  string `json:"base_ref,omitempty" default:"{{ context.git.base_ref }}"`
		BaseHash string `json:"base_hash,omitempty" default:"{{ context.git.base_hash }}"`
		Author   string `json:"author,omitempty" default:"{{ context.git.author }}"`
	}

	type SingleRecipe struct {
		Name      string                 `json:"name" validate:"required"`
		CellName  string                 `json:"cell_name,omitempty" default:"{{ context.workflow.cell }}"`
		CellPath  string                 `json:"cell_path,omitempty" default:"{{ context.workflow.cell_path }}"`
		Inputs    map[string]interface{} `json:"inputs"`
		Artifacts []interface{}          `json:"artifacts"`
		Git       SingleRecipeGit        `json:"git"`
	}

	type MultipleRecipes struct {
		GitRef  string         `json:"git_ref" default:"{{ context.git.ref }}" validate:"required"`
		Recipes []SingleRecipe `json:"recipes"`
	}

	inputMap := map[string]interface{}{
		"git_ref": "{{ inputs.git_ref }}",
		"recipes": []interface{}{
			map[string]interface{}{
				"name":      "child-simple",
				"inputs":    map[string]interface{}{"value": "one"},
				"artifacts": []interface{}{},
			},
			map[string]interface{}{
				"name":      "child-simple",
				"inputs":    map[string]interface{}{"value": "two"},
				"artifacts": []interface{}{},
			},
		},
	}

	err := InjectDefaults(reflect.TypeOf(MultipleRecipes{}), inputMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	recipes, ok := inputMap["recipes"].([]interface{})
	if !ok || len(recipes) != 2 {
		t.Fatalf("expected two recipes, got %v", inputMap["recipes"])
	}

	for i, raw := range recipes {
		rec := raw.(map[string]interface{})
		if rec["cell_name"] != "{{ context.workflow.cell }}" {
			t.Errorf("recipe %d missing cell_name default, got %v", i, rec["cell_name"])
		}
		if rec["cell_path"] != "{{ context.workflow.cell_path }}" {
			t.Errorf("recipe %d missing cell_path default, got %v", i, rec["cell_path"])
		}
		if rec["git"] == nil {
			t.Fatalf("recipe %d missing git map", i)
		}
		git := rec["git"].(map[string]interface{})
		if git["base_ref"] != "{{ context.git.base_ref }}" {
			t.Errorf("recipe %d missing git.base_ref default, got %v", i, git["base_ref"])
		}
	}
}
