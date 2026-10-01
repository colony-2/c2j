package testjob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeRuntimePersistsWorktreeAndObjects(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "recipe.yaml", `id: native
version: "1"
sequence:
- id: write
  op: command_execution
  inputs: {run: unused}
- id: read
  op: command_execution
  inputs: {run: "cat checkpoint.txt"}
- id: merge
  op: squashrebasemerge
  inputs: {commit_message: Fixture merge}
outputs:
  text: ${{ sequence.read.outputs.stdout }}
  session: ${{ sequence.write.outputs.session }}
  merged_hash: ${{ sequence.merge.outputs.merged_hash }}
`)
	writeDiscoveryFile(t, root, "native.test.yaml", `recipe: recipe.yaml
cases:
- id: durable
  type: integration_case
  runtime:
    observe_files: [checkpoint.txt, absent.txt]
  mocks:
    ops:
    - match: {node_path: native/write, op: command_execution}
      behavior:
        mode: return
        outputs: {success: true}
        effects:
          worktree: {checkpoint.txt: persisted}
          objects:
            session: {type: fixture.session/v1, metadata: {session_id: a}}
    - match: {node_path: native/read, op: command_execution}
      behavior: {mode: passthrough}
  assertions:
  - {type: cel_true, expr: "calls[0].worktree == {} && calls[1].worktree == {'checkpoint.txt': 'persisted'}"}
  - {type: cel_true, expr: "calls[0].outputs.session == outputs.session"}
  - {type: output_equals, path: text, value: persisted}
  - {type: output_equals, path: session.type, value: fixture.session/v1}
  - {type: op_call_count, node_path: native/write, value: 1}
  - {type: cel_true, expr: "size(outputs.merged_hash) == 40 && repositories.root.head == outputs.merged_hash && repositories.root.new_commits == 1 && repositories.root.files['checkpoint.txt'] == 'persisted' && repositories.root.clean"}
`)
	var output bytes.Buffer
	err := Run(context.Background(), Options{FilePath: filepath.Join(root, "native.test.yaml"), OutDir: filepath.Join(root, "results"), Stdout: &output, Execution: ExecutionOptions{Timeout: "10s"}})
	data, _ := os.ReadFile(filepath.Join(root, "results/summary.json"))
	require.NoError(t, err, "%s\n%s", output.String(), data)
}

func TestNativeRuntimeSubmitsAndAwaitsChild(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "child.yaml", `id: child
version: "1"
sequence: []
outputs: {accepted: true}
`)
	writeDiscoveryFile(t, root, "parent.yaml", `id: parent
version: "1"
sequence:
- id: submit
  op: extension_execution
  inputs:
    selector: fixture-model
    inputs: {sandbox: {type: shai}}
- id: await
  op: recipe.await_result_soft
  inputs: {job_id: "${{ sequence.submit.jobs.job_ids[0] }}"}
outputs:
  accepted: ${{ sequence.await.outputs.outputs.accepted }}
`)
	writeDiscoveryFile(t, root, "child.test.yaml", `recipe: parent.yaml
cases:
- id: children
  type: integration_case
  runtime:
    cells: {root: {}, service: {}}
  mocks:
    ops:
    - match: {node_path: parent/submit, selector: fixture-model}
      behavior:
        mode: return
        outputs: {success: true}
        effects:
          children: [{recipe: child.yaml, cell: service}]
  assertions:
  - {type: output_equals, path: accepted, value: true}
  - {type: op_call_count, node_path: parent/submit, value: 1}
`)
	var output bytes.Buffer
	err := Run(context.Background(), Options{FilePath: filepath.Join(root, "child.test.yaml"), OutDir: filepath.Join(root, "results"), Stdout: &output, Execution: ExecutionOptions{Timeout: "10s"}})
	data, _ := os.ReadFile(filepath.Join(root, "results/summary.json"))
	require.NoError(t, err, "%s\n%s", output.String(), data)
}

func TestNativeRuntimeRespondsToDocumentReview(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "feedback.md", "{++Keep this annotation++}")
	writeDiscoveryFile(t, root, "recipe.yaml", `id: review
version: "1"
sequence:
- id: draft
  op: command_execution
  inputs: {run: unused}
- id: approve
  op: input
  inputs:
    form:
      kind: review
      title: Review documents
      documents:
        design: '${{ sequence.draft.artifacts["design.md"] }}'
        plan: '${{ sequence.draft.artifacts["plan.md"] }}'
      fields:
      - {id: decision, type: short_answer, required: true, question: Decision}
      - {id: annotation, type: file_upload, required: true, question: Annotation}
outputs:
  decision: ${{ sequence.approve.outputs.fields.decision }}
  annotated: ${{ sequence.approve.outputs.fields.annotation.kind }}
`)
	writeDiscoveryFile(t, root, "review.test.yaml", `recipe: recipe.yaml
cases:
- id: review-documents
  type: integration_case
  runtime:
    responses:
    - node_path: review/approve
      fields: {decision: approve}
      attachments: {annotation: feedback.md}
  mocks:
    ops:
    - match: {node_path: review/draft}
      behavior:
        mode: return
        outputs: {success: true}
        artifacts: {design.md: '# Design', plan.md: '# Plan'}
  assertions:
  - {type: output_equals, path: decision, value: approve}
  - {type: output_equals, path: annotated, value: stored}
  - {type: review_document_exists, node_path: review/approve, path: design}
  - {type: review_document_exists, node_path: review/approve, path: plan}
`)
	var output bytes.Buffer
	err := Run(context.Background(), Options{FilePath: filepath.Join(root, "review.test.yaml"), OutDir: filepath.Join(root, "results"), Stdout: &output, Execution: ExecutionOptions{Timeout: "10s"}})
	data, _ := os.ReadFile(filepath.Join(root, "results/summary.json"))
	require.NoError(t, err, "%s\n%s", output.String(), data)
}

func TestNativeRuntimeFailuresAreReportedAndCasesAreIsolated(t *testing.T) {
	for _, mode := range []string{"unmocked", "unused", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeDiscoveryFile(t, root, "recipe.yaml", "id: work\nversion: '1'\nop: command_execution\ninputs: {run: 'sleep 5'}\n")
			suite := "recipe: recipe.yaml\ncases:\n- id: failure\n  type: integration_case\n  runtime: {}\n"
			switch mode {
			case "unused":
				suite += "  mocks: {ops: [{match: {op: command_execution}, behavior: {mode: return}}, {match: {op: command_execution}, behavior: {mode: return}}]}\n"
			case "failure":
				suite += "  mocks: {ops: [{match: {op: command_execution}, behavior: {mode: fail, error: {message: expected}}}]}\n"
			case "timeout":
				suite += "  mocks: {ops: [{match: {op: command_execution}, behavior: {mode: passthrough}}]}\n"
			}
			writeDiscoveryFile(t, root, "suite.test.yaml", suite)
			var output bytes.Buffer
			err := Run(context.Background(), Options{FilePath: filepath.Join(root, "suite.test.yaml"), OutDir: filepath.Join(root, "results"), Stdout: &output, Execution: ExecutionOptions{Timeout: "300ms"}})
			require.Error(t, err)
			require.FileExists(t, filepath.Join(root, "results/summary.json"))
		})
	}
	// Each case gets its own Git repository/runtime, even when scheduled together.
	root := t.TempDir()
	writeDiscoveryFile(t, root, "recipe.yaml", "id: work\nversion: '1'\nop: command_execution\ninputs: {run: 'test ! -f marker && touch marker'}\n")
	writeDiscoveryFile(t, root, "suite.test.yaml", `recipe: recipe.yaml
cases:
- &case
  id: first
  type: integration_case
  runtime: {}
  mocks: {ops: [{match: {op: command_execution}, behavior: {mode: passthrough}}]}
- <<: *case
  id: second
`)
	var output bytes.Buffer
	require.NoError(t, Run(context.Background(), Options{FilePath: filepath.Join(root, "suite.test.yaml"), OutDir: filepath.Join(root, "results"), Parallelism: 2, Stdout: &output}), output.String())
}

func TestNativeRuntimeHostCommandsAndRepeatedFixtures(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "files/seed.txt", "fixture tree")
	writeDiscoveryFile(t, root, "recipe.yaml", `id: commands
version: "1"
sequence:
- id: one
  op: command_execution
  inputs:
    sandbox: {type: shai}
    env: {SOURCE: "{{ context.environment.op.worktree_path }}/seed.txt"}
    run: 'cat "$SOURCE"'
- id: two
  op: command_execution
  inputs: {run: "cat seed.txt"}
outputs:
  one: ${{ sequence.one.outputs.stdout }}
  two: ${{ sequence.two.outputs.stdout }}
`)
	writeDiscoveryFile(t, root, "suite.test.yaml", `recipe: recipe.yaml
cases:
- id: host
  type: integration_case
  runtime:
    command_sandbox: none
    cells:
      root:
        file_sources: {".": files}
  mocks:
    ops:
    - repeat: true
      match: {op: command_execution}
      behavior: {mode: passthrough}
  assertions:
  - {type: output_equals, path: one, value: fixture tree}
  - {type: output_equals, path: two, value: fixture tree}
`)
	var output bytes.Buffer
	err := Run(context.Background(), Options{FilePath: filepath.Join(root, "suite.test.yaml"), OutDir: filepath.Join(root, "results"), Stdout: &output})
	data, _ := os.ReadFile(filepath.Join(root, "results/summary.json"))
	require.NoError(t, err, "%s\n%s", output.String(), data)
}
