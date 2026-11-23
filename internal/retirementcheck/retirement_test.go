// Package retirementcheck verifies the delivered consumer boundary, including
// private test types, aliases, inherited ports and anonymous compositions.
package retirementcheck

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestC04ConsumerMethodInventory(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	moduleCmd := exec.Command("go", "list", "-m", "-json", "github.com/colony-2/jobdb")
	moduleCmd.Dir = root
	moduleBytes, err := moduleCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var dependency struct {
		Version string
		Replace *json.RawMessage
	}
	if err := json.Unmarshal(moduleBytes, &dependency); err != nil {
		t.Fatal(err)
	}
	if dependency.Version != "v0.0.99-0.20261122051819-86f7ba9bc009" || dependency.Replace != nil {
		t.Fatalf("unexpected JobDB resolution: %s", moduleBytes)
	}
	cmd := exec.Command("go", "list", "-p=2", "-tags=integration", "-deps", "-test", "-export", "-json", "./...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatal(string(e.Stderr))
		}
		t.Fatal(err)
	}
	type pkg struct {
		ImportPath, Dir, Name, Export string
		GoFiles                       []string
		ImportMap                     map[string]string
	}
	var packages []pkg
	exports := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var p pkg
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p)
		exports[p.ImportPath] = p.Export
	}
	// Scan sources outside package discovery too: archived fixture generators,
	// build-tagged helpers, hidden guidance bundles and shipped harness templates.
	sourceFiles := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fs := token.NewFileSet()
		file, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			return err
		}
		sourceFiles++
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			imported, _ := strconv.Unquote(imp.Path.Value)
			if imported == "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote" {
				alias := "remote"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				aliases[alias] = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if _, ok := n.(*ast.TypeAssertExpr); ok {
				return false
			} // Negative capability assertions are evidence.
			if decl, ok := n.(*ast.FuncDecl); ok && (decl.Name.Name == "AddGoalCandidate" || decl.Name.Name == "AttachJobToGoal") {
				t.Errorf("attachment declaration at %s", fs.Position(n.Pos()))
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] && (sel.Sel.Name == "NewServer" || sel.Sel.Name == "NewServerWithOptions") {
					t.Errorf("legacy HTTP factory at %s", fs.Position(n.Pos()))
				}
				if sel.Sel.Name == "WorkflowRuntime" || sel.Sel.Name == "GoalCandidateMutator" || sel.Sel.Name == "GoalRuntime" {
					t.Errorf("attachment-bearing input at %s", fs.Position(n.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	count, anonymous, checked := 0, 0, 0
	fset := token.NewFileSet()
	lookup := func(path string) (io.ReadCloser, error) { return os.Open(exports[path]) }
	sharedImporter := importer.ForCompiler(fset, "gc", lookup)
	for _, p := range packages {
		if !strings.HasPrefix(p.ImportPath, "github.com/colony-2/c2j/") || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			if !filepath.IsAbs(name) {
				name = filepath.Join(p.Dir, name)
			}
			file, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
		}
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
		config := &types.Config{Importer: sharedImporter}
		typed, err := config.Check(p.ImportPath, fset, files, info)
		if err != nil {
			t.Fatal(err)
		}
		check := func(typ types.Type, location string) {
			for _, candidate := range []types.Type{typ, types.NewPointer(typ)} {
				methods := types.NewMethodSet(candidate)
				for _, method := range []string{"AddGoalCandidate", "AttachJobToGoal"} {
					if methods.Lookup(nil, method) != nil {
						t.Errorf("%s exposes %s on %s", location, method, candidate)
					}
				}
			}
		}
		for _, name := range typed.Scope().Names() {
			if obj, ok := typed.Scope().Lookup(name).(*types.TypeName); ok {
				check(obj.Type(), p.ImportPath+"."+name)
				count++
			}
		}
		for expr, value := range info.Types {
			switch expr.(type) {
			case *ast.StructType, *ast.InterfaceType:
				check(value.Type, fset.Position(expr.Pos()).String())
				anonymous++
			}
		}
		checked++
	}
	if checked < 100 || count < 100 {
		t.Fatalf("incomplete inventory: %d packages, %d named types", checked, count)
	}
	t.Logf("Scanned %d Go files including generators; checked %d package variants, %d named value/pointer method sets and %d anonymous struct/interface compositions (integration files included)", sourceFiles, checked, count, anonymous)
}
