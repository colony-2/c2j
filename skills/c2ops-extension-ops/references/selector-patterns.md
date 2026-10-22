<!-- Sources: colony-2/c2ops 8ac6ae9009f621bdc8065eaaed06e0852c67006b op.yaml schemas; pkg/ops/extensions/execution_op.go; c2j recipe template/artifact references. -->

# Selector Patterns

## Base Invocation

Use a package selector directly in `op:` with ordinary `inputs`, or use `extension_execution` and put op-specific parameters under nested `inputs`. See [the catalog](trusted-c2ops.md) for the audited revision and package names.

```yaml
sequence:
  - id: ask
    op: extension_execution
    inputs:
      selector: nix:github:colony-2/c2ops/published#llm
      inputs:
        provider: openai
        model: gpt-4.1-mini
        prompt: "{{ inputs.prompt }}"
outputs:
  response: "{{ sequence.ask.outputs.response }}"
```

If the extension manifest declares defaults, c2j applies them before execution. The extension validates nested inputs against its `input_schema` and validates stdout against `output_schema`.

## Codex

```yaml
- id: codex
  op: extension_execution
  inputs:
    selector: nix:github:colony-2/c2ops/published#codex
    inputs:
      prompt: "{{ inputs.prompt }}"
```

For skill execution through c2ops:

```yaml
- id: run_skill
  op: extension_execution
  inputs:
    selector: nix:github:colony-2/c2ops/published#skill-run
    inputs:
      skill: c2j-recipes
      prompt: "{{ inputs.prompt }}"
```

## Rule Gate

```yaml
state:
  initial: gate
  states:
    gate:
      op: extension_execution
      inputs:
        selector: nix:github:colony-2/c2ops/published#rule_gate
        inputs:
          rules:
            - id: approved
              type: assert
              value: "${{ inputs.approved }}"
              message: Approval is required
      transitions:
        - to: pass
          when: "states.gate.outputs.ok == true"
        - to: fail
          when: "states.gate.outputs.ok != true"
```

## GitHub Actions

Use `gha` for one workflow-style action and `gha-many` for fan-out. Pass structured inputs as raw template values, not JSON strings, unless the op schema specifically expects a string.

```yaml
- id: ci
  op: extension_execution
  inputs:
    selector: nix:github:colony-2/c2ops/published#gha
    inputs:
      workflow: .github/workflows/ci.yml
      with: "${{ inputs.workflow_inputs }}"
```

## Pydantic

Use `pydantic` for PydanticAI-backed LLM inference with the llm2-style contract; it is not a generic validator accepting a `data` input.

```yaml
- id: validate
  op: extension_execution
  inputs:
    selector: nix:github:colony-2/c2ops/published#pydantic
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review the implementation and summarize risks.
```

## Kimi and Jev

`kimi` runs the Kimi Code CLI. Its resume input is `sessionId`, not a Codex session object.

```yaml
- id: code
  op: nix:github:colony-2/c2ops/published#kimi
  inputs:
    prompt: Fix the failing tests.
    worktree_path: "${{ context.environment.op.worktree_path }}"
```

`jev` evaluates state with typed questions; results are under `outputs.answers`.

```yaml
- id: assess
  op: nix:github:colony-2/c2ops/published#jev
  inputs:
    state: "${{ inputs.change_summary }}"
    questions:
      ready:
        type: noul
        instructions: Is the change ready to ship?
```

## Artifacts

Bind artifacts at the recipe node level, then pass op-visible inbox paths in nested inputs.

```yaml
- id: analyze
  op: extension_execution
  artifacts:
    report.md: '${{ context.artifacts["report.md"] }}'
  inputs:
    selector: nix:github:colony-2/c2ops/published#codex
    inputs:
      prompt: 'Review "${{ context.environment.op.inbox }}/report.md".'
```

## Worker Environment

Extensions execute directly in the c2j worker environment. Provision tools,
credentials, mounts, and any container isolation before starting the worker.
Remove former generic `sandbox` inputs, including `type: none`, and parameters
used only to forward them. c2j no longer strips this field from extension inputs;
input validation follows the extension's schema, and permissive schemas can pass
it through as ordinary data. It never configures c2j isolation.

## Immutable Session Objects

For an op version that implements objects, route `session: "${{ sequence.previous.outputs.session }}"` as an ordinary input. The audited Codex and skill.run manifests declare `x-c2j-object-type: c2ops.codex.session/v1` on `session`. Resume them with that object; `sessionId` is an output identifier, not a supported resume input. Aider and Kimi have different, legacy sessionId contracts.

Extension manifests annotate checkpoint fields with `type: object` and `x-c2j-object-type: c2ops.codex.session/v1`. Before invoking the process, c2j replaces each reference with `{ref, metadata, files}`. File paths are private writable copies in the op's filesystem view. An explicit object must be the complete source of resume state; do not overlay a shared session-ID cache or an implicit latest checkpoint.

To publish, write export parts beneath `C2J_OBJECT_OUTBOX` and emit `{"output":{"session":{"$object":"next"}},"objects":{"next":{"type":"c2ops.codex.session/v1","metadata":{"session_id":"..."},"files":{"home":"<absolute staging path>"}}}}`. c2j seals the files and replaces the marker with the durable reference. Returning the input descriptor's `ref` preserves the original checkpoint. Keep ordinary user deliverables in the regular artifact outbox.

Both later consumers may select the same earlier object; each starts with its exact state. Session IDs may match while checkpoint contents differ. A Codex export must preserve all supported resumable assets, including required database/rollout state, and omit credentials and transient files. Resolve relocated paths inside the adapter. Test branching, retries and worker restart without shared caches before changing recipe routing.
