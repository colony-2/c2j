// Run with: go run ./testdata/recipes/verify-hook/state-check.go /tmp/verify-state-evidence
// This opt-in acceptance helper executes the production command bodies separately
// in a disposable candidate, preserving real dirty tracked and untracked files.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func write(path string, data []byte) {
	must(os.MkdirAll(filepath.Dir(path), 0700))
	must(os.WriteFile(path, data, 0600))
}
func run(dir string, args ...string) []byte {
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	b, err := c.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%v: %v: %s", args, err, b))
	}
	return b
}
func snapshot(root string) map[string]any {
	files := map[string]string{}
	must(filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	}))
	return map[string]any{"status": string(run(root, "git", "status", "--porcelain=v1", "--untracked-files=all")), "diff": string(run(root, "git", "diff", "--binary")), "files": files}
}
func main() {
	ops.Register(commandop.GetOp())
	if len(os.Args) != 2 {
		panic("supply an evidence directory outside the candidate")
	}
	evidence, err := filepath.Abs(os.Args[1])
	must(err)
	must(os.MkdirAll(evidence, 0700))
	data, err := os.ReadFile(".c2/recipes/hooks/build/verify.yaml")
	must(err)
	r, err := recipe.LoadRecipeFromString(data)
	must(err)
	states := r.RecipeImpl.(*recipe.RecipeState).States.States
	root, err := os.MkdirTemp("", "verify-state-candidate-")
	must(err)
	defer os.RemoveAll(root)
	for p, s := range map[string]string{"go.mod": "module example.com/statefixture\n\ngo 1.26\n", "fixture.go": "package fixture\n", "fixture_test.go": "package fixture\nimport \"testing\"\nfunc TestDocker(t *testing.T) { t.Skip(\"optional Docker coverage\") }\n", "Makefile": "check-mod:\n\tgo mod tidy -diff\n", "skills/fixture/SKILL.md": "---\nname: state-fixture\ndescription: Disposable candidate state check.\n---\n\nUse only in acceptance fixtures.\n"} {
		write(filepath.Join(root, p), []byte(s))
	}
	run(root, "git", "init", "-q")
	run(root, "git", "config", "user.email", "fixture@example.invalid")
	run(root, "git", "config", "user.name", "Fixture")
	run(root, "git", "add", ".")
	run(root, "git", "commit", "-qm", "Seed fixture")
	write(filepath.Join(root, "fixture.go"), []byte("package fixture\n// Existing tracked candidate edit.\n"))
	write(filepath.Join(root, "untracked-candidate.txt"), []byte("preserve existing untracked bytes\n"))
	before, _ := json.MarshalIndent(snapshot(root), "", "  ")
	write(filepath.Join(evidence, "before.json"), before)
	names := []string{"check_environment", "check_modules", "test_integration", "check_skills", "summarize"}
	logs := []string{"environment.log", "modules.log", "tests.log", "skills.log"}
	results := []string{}
	inbox := filepath.Join(evidence, "report-inbox")
	must(os.MkdirAll(inbox, 0700))
	for i, n := range names {
		node := states[n].Node.NodeImpl.(*recipe.NodeOp)
		body := node.Inputs["run"].(string)
		limit, err := time.ParseDuration(node.Inputs["timeout"].(string))
		must(err)
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		outbox := filepath.Join(evidence, n)
		must(os.MkdirAll(outbox, 0700))
		c := exec.CommandContext(ctx, "bash", "-c", body)
		c.Dir = root
		c.Env = append(os.Environ(), "GOPRIVATE=github.com/colony-2/*", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "OUTBOX="+outbox, "INBOX="+inbox, "API_VERSION=c2.build-hook/v1", "CANDIDATE=dirty tracked and untracked fixture", "BASE=seed", "VERDICT=passed")
		for j, detail := range results {
			c.Env = append(c.Env, fmt.Sprintf("DETAIL_%d=%s", j, detail), fmt.Sprintf("RESULT_%d=passed", j))
		}
		b, runErr := c.CombinedOutput()
		cancel()
		write(filepath.Join(outbox, "stdout.txt"), b)
		if runErr != nil {
			panic(fmt.Sprintf("%s failed: %v; inspect %s", n, runErr, outbox))
		}
		results = append(results, `{"success":true,"exit_code":0,"timed_out":false}`)
		if i < 4 {
			b, err := os.ReadFile(filepath.Join(outbox, logs[i]))
			must(err)
			write(filepath.Join(inbox, logs[i]), b)
		}
		after, _ := json.MarshalIndent(snapshot(root), "", "  ")
		write(filepath.Join(outbox, "candidate-state.json"), after)
		if string(before) != string(after) {
			panic("candidate state changed during " + n)
		}
	}
	summary, err := os.ReadFile(filepath.Join(evidence, "summarize", "summary.md"))
	must(err)
	if !strings.Contains(string(summary), "SKIP: TestDocker") {
		panic("skip evidence missing")
	}
	fmt.Println("PASS: all five production command bodies succeeded; dirty tracked and untracked state unchanged after every task; optional skip visible.")
}
