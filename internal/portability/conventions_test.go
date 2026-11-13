// These checks deliberately cover the inventoried generators, not arbitrary Go
// or shell programs. Keep their supported construction forms explicit.
package portability

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/shellcmd"
)

const module = "github.com/colony-2/c2j/"

type adoption struct {
	file      string
	functions []string
	identity  string
}

var inventory = []adoption{
	{"pkg/toolenv/environment.go", []string{"Manager.Prepare", "writeRunners"}, ""},
	{"pkg/toolenv/runner_dispatch_test.go", []string{"seedRunnerTool", "testDeclaredRunnerDispatch", "TestUndeclaredRunnerFallback"}, "seedRunnerTool"},
	{"pkg/toolenv/environment_test.go", []string{"fakeManagers", "TestNixPrebuiltOnlyFailure"}, ""},
	{"pkg/recipetest/nix_passthrough_test.go", []string{"newNixPassthroughFixture", "nixPassthroughFixture.prepare"}, "nixPassthroughFixture.prepare"},
	{"pkg/recipetest/repository_packages_test.go", []string{"TestRepositoryPackagesPassthroughAndMockBoundary", "TestDeclaredRunnerPassthroughAndMockBoundary"}, ""},
	{"pkg/worker/compiler/tool_setup_test.go", []string{"TestLazyToolSetupScopeAndReplay", "TestToolRecoveryResumesCurrentStep"}, ""},
}

type site struct {
	file, function string
	line           int
}

// No production exemption is needed. A future deliberate portability test must
// list its exact file/function/line here AND explain itself at that source line.
var allowedEchoTests = map[site]bool{}

type checker struct {
	set        *token.FileSet
	file       string
	function   string
	imports    map[string]string
	errors     []string
	exceptions map[site]bool
	comments   map[int]string
}

func (c *checker) report(pos token.Pos, message string) {
	issue := fmt.Sprintf("%s:%d (%s): %s", c.file, c.set.Position(pos).Line, c.function, message)
	for _, previous := range c.errors {
		if previous == issue {
			return
		}
	}
	c.errors = append(c.errors, issue)
}

func functionName(f *ast.FuncDecl) string {
	if f.Recv != nil {
		x := f.Recv.List[0].Type
		if p, ok := x.(*ast.StarExpr); ok {
			x = p.X
		}
		if id, ok := x.(*ast.Ident); ok {
			return id.Name + "." + f.Name.Name
		}
	}
	return f.Name.Name
}

func (c *checker) callName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return c.imports[id.Name] + "." + sel.Sel.Name
		}
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func literal(e ast.Expr) string {
	if s, ok := e.(*ast.BasicLit); ok && s.Kind == token.STRING {
		text, _ := strconv.Unquote(s.Value)
		return text
	}
	return ""
}

// text approximates only the supported string builders. Dynamic values become
// inert words; their constructing assignments/closures are inspected separately.
// Unknown calls in script expressions fail closed rather than hiding source.
func (c *checker) text(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.BasicLit:
		return literal(e)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			c.report(e.Pos(), "unsupported script expression; extend the checked builder forms")
		}
		return c.text(e.X) + c.text(e.Y)
	case *ast.ParenExpr:
		return c.text(e.X)
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr:
		return "PORTABILITY_VALUE"
	case *ast.CallExpr:
		name := c.callName(e)
		if len(e.Args) == 1 {
			switch name {
			case module + "pkg/shellcmd.QuotePOSIX":
				return shellcmd.QuotePOSIX(c.text(e.Args[0]))
			case module + "pkg/shellcmd.Literal", module + "pkg/shellcmd.LiteralLine":
				value := c.text(e.Args[0])
				if strings.HasPrefix(value, "#!/") {
					c.script(e.Args[0].Pos(), value)
				}
				if strings.HasSuffix(name, ".LiteralLine") {
					return shellcmd.LiteralLine(value)
				}
				return shellcmd.Literal(value)
			}
			if a, ok := e.Fun.(*ast.ArrayType); ok && literalType(a.Elt, "byte") {
				return c.text(e.Args[0])
			}
		}
		switch name {
		case "path/filepath.Join", "path/filepath.Base", "ordinary":
			return "PORTABILITY_VALUE"
		}
		c.report(e.Pos(), "unsupported script builder "+name+"; use shellcmd helpers or extend inspection")
		return "PORTABILITY_VALUE"
	default:
		c.report(e.Pos(), "unsupported script construction; extend inspection")
		return "PORTABILITY_VALUE"
	}
}

func literalType(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func (c *checker) expression(e ast.Expr) {
	if m, ok := e.(*ast.CompositeLit); ok {
		for _, elt := range m.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				c.expression(kv.Value)
			} else {
				c.expression(elt)
			}
		}
		return
	}
	c.script(e.Pos(), c.text(e))
}

// shellWords preserves quote/comment boundaries and command separators. It is
// intentionally a lexer for these templates, not a general shell parser.
type word struct {
	text      string
	separator bool
}

func shellWords(script string) []word {
	var words []word
	var b strings.Builder
	started := false
	quote := byte(0)
	flush := func() {
		if started {
			words = append(words, word{text: b.String()})
			b.Reset()
			started = false
		}
	}
	for i := 0; i < len(script); i++ {
		ch := script[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if ch == '\\' && quote == '"' && i+1 < len(script) && strings.ContainsRune("$`\"\\\n", rune(script[i+1])) {
				i++
				b.WriteByte(script[i])
			} else {
				b.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			started = true
		case '\\':
			if i+1 < len(script) {
				i++
				if script[i] != '\n' {
					b.WriteByte(script[i])
					started = true
				}
			}
		case '#':
			if !started {
				for i < len(script) && script[i] != '\n' {
					i++
				}
				if i < len(script) {
					words = append(words, word{text: "\n", separator: true})
				}
			} else {
				b.WriteByte(ch)
			}
		case ' ', '\t', '\r':
			flush()
		case '\n', ';', '|', '&', '(', ')', '{', '}':
			flush()
			words = append(words, word{text: string(ch), separator: true})
		default:
			b.WriteByte(ch)
			started = true
		}
	}
	flush()
	return words
}

func (c *checker) script(pos token.Pos, script string) {
	command := true
	for _, w := range shellWords(script) {
		if w.separator {
			command = true
			continue
		}
		// Old nested printf executable templates encode newlines in their format.
		if strings.HasPrefix(w.text, "#!/") {
			nested := strings.ReplaceAll(w.text, `\n`, "\n")
			if nested != script {
				c.script(pos, nested)
			}
		}
		if !command {
			continue
		}
		switch w.text {
		case "if", "then", "else", "elif", "do", "!":
			continue
		case "echo":
			where := site{c.file, c.function, c.set.Position(pos).Line}
			reason := strings.TrimSpace(strings.TrimPrefix(c.comments[where.line], "portability:allow-echo"))
			if !c.exceptions[where] || !strings.HasSuffix(where.file, "_test.go") || !strings.HasPrefix(where.function, "Test") || !strings.HasPrefix(c.comments[where.line], "portability:allow-echo") || reason == "" {
				c.report(pos, "direct echo command; use shellcmd.Literal or LiteralLine (runtime data: fixed-format printf)")
			}
		}
		// Environment assignments before a command do not consume its position.
		if strings.Contains(w.text, "=") && !strings.HasPrefix(w.text, "=") {
			continue
		}
		command = false
	}
}

func inspectSource(a adoption, source []byte, exceptions map[site]bool) []string {
	set := token.NewFileSet()
	f, err := parser.ParseFile(set, a.file, source, parser.ParseComments)
	if err != nil {
		return []string{err.Error()}
	}
	c := &checker{set: set, file: a.file, imports: map[string]string{}, exceptions: exceptions, comments: map[int]string{}}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		c.imports[name] = path
	}
	for _, group := range f.Comments {
		for _, comment := range group.List {
			c.comments[set.Position(comment.Pos()).Line] = strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		}
	}
	found := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		c.function = functionName(fn)
		// Catch a replacement helper introduced outside the inventoried generators.
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := c.callName(call)
			if (name == "strings.ReplaceAll" || name == "strings.Replace") && len(call.Args) >= 3 && literal(call.Args[1]) == "'" {
				c.report(call.Pos(), "local apostrophe quoting; use shellcmd.QuotePOSIX")
			}
			return true
		})
		selected := false
		for _, name := range a.functions {
			if c.function == name {
				selected = true
			}
		}
		if !selected {
			continue
		}
		found[c.function] = true
		identity, scripts := false, 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				name := c.callName(n)
				if name == module+"internal/pathutil.CanonicalExisting" {
					identity = true
				}
				if c.function == a.identity && (name == "path/filepath.EvalSymlinks" || name == "path/filepath.Abs" || name == "path/filepath.Clean") {
					c.report(n.Pos(), "local fixture normalization; use pathutil.CanonicalExisting after creating the target")
				}
				if name == "quote" || name == "shellQuote" || name == "quotePOSIX" {
					c.report(n.Pos(), "local quoting helper; use shellcmd.QuotePOSIX")
				}
				if (name == "writeExecutable" || name == "os.WriteFile" || name == "write") && len(n.Args) >= 2 {
					// Manifests and config byte arrays are data, not generated scripts.
					if name != "os.WriteFile" || strings.Contains(sourceText(n.Args[1]), "#!/") {
						c.expression(n.Args[1])
						scripts++
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i < len(n.Rhs) && scriptVariable(lhs) {
						c.expression(n.Rhs[i])
						scripts++
					}
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if i < len(n.Values) && scriptVariable(name) {
						c.expression(n.Values[i])
						scripts++
					}
				}
			case *ast.ReturnStmt:
				if c.function == "writeRunners" {
					for _, expr := range n.Results {
						if sourceText(expr) != "" {
							c.expression(expr)
							scripts++
						}
					}
				}
			case *ast.BasicLit:
				// Includes nested executable templates, even inside a printf format.
				if strings.HasPrefix(literal(n), "#!/") {
					c.script(n.Pos(), literal(n))
					scripts++
				}
			}
			return true
		})
		if c.function == a.identity && !identity {
			c.report(fn.Pos(), "missing pathutil.CanonicalExisting adoption")
		}
		if scripts == 0 {
			c.report(fn.Pos(), "inventoried generator has no inspected scripts; update inspection")
		}
	}
	for _, name := range a.functions {
		if !found[name] {
			c.function = name
			c.report(f.Pos(), "missing inventoried generator")
		}
	}
	return c.errors
}

// Include indexed appends such as scripts[runner] += fragment.
func scriptVariable(expr ast.Expr) bool {
	if indexed, ok := expr.(*ast.IndexExpr); ok {
		return scriptVariable(indexed.X)
	}
	id, ok := expr.(*ast.Ident)
	return ok && (id.Name == "script" || id.Name == "scripts" || id.Name == "body" || id.Name == "branch")
}

// Only used to recognize sink shape, not to parse Go source or shell commands.
func sourceText(expr ast.Expr) string {
	var b strings.Builder
	ast.Inspect(expr, func(n ast.Node) bool {
		if s, ok := n.(*ast.BasicLit); ok {
			b.WriteString(literal(s))
		}
		return true
	})
	return b.String()
}

func TestAdoptedConventions(t *testing.T) {
	for _, a := range inventory {
		t.Run(a.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("../..", a.file))
			if err != nil {
				t.Fatal(err)
			}
			for _, issue := range inspectSource(a, source, allowedEchoTests) {
				t.Error(issue)
			}
		})
	}
}

func TestEnforcementRejectsBypasses(t *testing.T) {
	for _, tc := range []struct{ name, body, diagnostic string }{
		{"local apostrophe quote", `script := "#!/bin/sh\n" + q("data")
funcDummy := func(s string) string { return "'" + str.ReplaceAll(s, "'", "'\"'\"'") + "'" }; _ = funcDummy`, "shellcmd.QuotePOSIX"},
		{"direct fixture normalization", `identity, _ := fp.EvalSymlinks("target"); _ = identity; script := "#!/bin/sh\n"`, "local fixture normalization"},
		{"missing identity helper", `script := "#!/bin/sh\n"`, "missing pathutil.CanonicalExisting"},
		{"inline echo", `_, _ = pu.CanonicalExisting("target"); script := "#!/bin/sh\necho broken\n"`, "direct echo command"},
		{"after separator", `_, _ = pu.CanonicalExisting("target"); script := "#!/bin/sh\nprintf '%s' ok; echo broken\n"`, "direct echo command"},
		{"indexed script append", `_, _ = pu.CanonicalExisting("target"); scripts := map[string]string{"probe":"#!/bin/sh\n"}; scripts["probe"] += "echo broken\n"; script := scripts["probe"]`, "direct echo command"},
		{"declared script", `_, _ = pu.CanonicalExisting("target"); var script = "echo broken\n"`, "direct echo command"},
		{"control flow", `_, _ = pu.CanonicalExisting("target"); script := "#!/bin/sh\nif true; then echo broken; fi\n"`, "direct echo command"},
		{"nested executable", `_, _ = pu.CanonicalExisting("target"); script := "#!/bin/sh\nprintf '#!/bin/sh\\necho broken\\n' > tool\n"`, "direct echo command"},
		{"nested shared literal", `_, _ = pu.CanonicalExisting("target"); script := "#!/bin/sh\n" + sc.Literal("#!/bin/sh\necho broken\n") + " > tool\n"`, "direct echo command"},
		{"unsupported builder", `_, _ = pu.CanonicalExisting("target"); script := fmt.Sprintf("#!/bin/sh\n%s", "echo broken")`, "unsupported script builder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := `package fixture
import (pu "github.com/colony-2/c2j/internal/pathutil"; sc "github.com/colony-2/c2j/pkg/shellcmd"; str "strings"; fp "path/filepath"; "fmt")
func TestFixture() {
` + tc.body + `
_ = script
}`
			a := adoption{"fixture_test.go", []string{"TestFixture"}, "TestFixture"}
			issues := inspectSource(a, []byte(source), nil)
			joined := strings.Join(issues, "\n")
			if !strings.Contains(joined, tc.diagnostic) || !strings.Contains(joined, "fixture_test.go:") || !strings.Contains(joined, "(TestFixture)") {
				t.Fatalf("missing located %q diagnostic: %v", tc.diagnostic, issues)
			}
			t.Log(joined)
		})
	}
}

func TestEnforcementAcceptsDataAndSharedHelpers(t *testing.T) {
	source := "package fixture\n" + `import (pu "github.com/colony-2/c2j/internal/pathutil"; sc "github.com/colony-2/c2j/pkg/shellcmd")
func TestFixture() {
 _, _ = pu.CanonicalExisting("target")
 args := []string{"` + "`echo injected`" + `", "echo literal"}; _ = args
 script := "#!/bin/sh\n# echo in a comment\nprintf '%s' 'echo data'\n" + sc.LiteralLine("echo shared data") + "\n" + sc.QuotePOSIX("argument")
 _ = script
}`
	issues := inspectSource(adoption{"fixture_test.go", []string{"TestFixture"}, "TestFixture"}, []byte(source), nil)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
}

func TestEchoExceptionsAreExactAndExplained(t *testing.T) {
	for _, tc := range []struct {
		name, file, function, reason string
		line                         int
		accept                       bool
	}{
		{"explicit test site", "fixture_test.go", "TestPortability", "compare shell echo behavior", 3, true},
		{"blank reason", "fixture_test.go", "TestPortability", "", 3, false},
		{"different line", "fixture_test.go", "TestPortability", "compare shells", 4, false},
		{"different function", "fixture_test.go", "TestOther", "compare shells", 3, false},
		{"non-test file", "fixture.go", "TestPortability", "compare shells", 3, false},
		{"non-test function", "fixture_test.go", "generator", "compare shells", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package fixture\nfunc " + tc.function + "() {\nscript := \"#!/bin/sh\\necho portability\\n\" // portability:allow-echo " + tc.reason + "\n_ = script\n}"
			allowed := map[site]bool{{"fixture_test.go", "TestPortability", tc.line}: true}
			issues := inspectSource(adoption{tc.file, []string{tc.function}, ""}, []byte(source), allowed)
			if (len(issues) == 0) != tc.accept {
				t.Fatalf("accept=%v, diagnostics=%v", tc.accept, issues)
			}
		})
	}
}

func TestEnforcementRequiresInventoriedGenerators(t *testing.T) {
	for _, source := range []string{"package fixture", "package fixture\nfunc TestFixture() {}"} {
		issues := inspectSource(adoption{"fixture_test.go", []string{"TestFixture"}, ""}, []byte(source), nil)
		if len(issues) == 0 {
			t.Fatal("silently omitted inventoried generator")
		}
	}
}
