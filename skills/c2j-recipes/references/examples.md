<!-- Sources: pkg/worker/test-fixtures/recipes/*.yaml; README.md. -->

# Examples

## Minimal Command Recipe

```yaml
id: echo-example
version: "1.0.0"
desc: Echo a submitted message
input_schema:
  message:
    type: string
    default_value: hello
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "printf '%s' '{{ inputs.message }}'"
outputs:
  stdout: "{{ sequence.echo.outputs.stdout }}"
```

Run:

```bash
c2j submit --recipe-file ./echo.yaml --inputs-json '{"message":"hi"}' --run --embed
```

## Artifact Flow

```yaml
id: artifact-flow
version: "1.0.0"
input_schema: {}
sequence:
  - id: emit
    op: test_emit_artifact
  - id: consume
    op: test_consume_artifact
    inputs:
      artifact: '${{ sequence.emit.artifacts["foo"] }}'
outputs:
  emitted: "{{ sequence.emit.outputs.name }}"
  consumed: "{{ sequence.consume.outputs.name }}"
```

## Failure Continue

```yaml
id: optional-step
version: "1.0.0"
input_schema: {}
sequence:
  - id: optional
    op: error-activity
    inputs:
      message: optional data
      error: true
    catch:
      - id: substitute_optional_data
        when: failure_message_contains(failure, "simulated error")
        continue:
          outputs:
            output: synthetic-data
            status: recovered
  - id: consume
    op: echo_activity
    inputs:
      message: '${{ "using " + sequence.optional.outputs.output }}'
outputs:
  optional_status: "${{ sequence.optional.outputs.status }}"
  consumed: "${{ sequence.consume.outputs.output }}"
```

## Failure Route

```yaml
id: failure-route
version: "1.0"
input_schema: {}
state:
  initial: work
  states:
    work:
      op: error-activity
      inputs:
        message: attempt work
        error: true
      catch:
        - id: simulated_error_to_review
          when: failure_message_contains(failure, "simulated error")
          to: review
          payload:
            handled_kind: "${{ failure.kind }}"
    review:
      op: echo_activity
      inputs:
        message: '${{ "handled " + transition.failure.kind + " from " + transition.from }}'
outputs:
  review_output: "${{ states.review.outputs.output }}"
```

## Nested State In Sequence

```yaml
sequence:
  - id: processor
    state:
      initial: validate
      states:
        validate:
          sequence:
            - id: check
              op: command_execution
              inputs:
                run: "printf ok"
          outputs:
            status: "{{ sequence.check.outputs.stdout }}"
          transitions:
            - to: done
        done:
          op: command_execution
          inputs:
            run: "printf complete"
outputs:
  status: "{{ sequence.processor.outputs.status }}"
```

Use test-only ops only in fixture scenarios. Use built-in or selector-backed ops in production recipes.
