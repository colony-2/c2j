# Artifacts from values

Create inbox files directly from recipe values with `kind: value`. This requires a runtime containing this feature; older releases support only stored and external artifact references.

## Writing a document into an op's inbox

Use a value source in an explicit artifact binding. This example uses Python 3
to report the file's byte count:

```yaml
id: value-document-example
version: "1"
input_schema:
  document:
    type: string
    required: true
inputs:
  document: ${{ inputs.document }}
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      context.txt:
        kind: value
        format: text
        value: ${{ inputs.document }}
    inputs:
      working_directory: ${{ context.environment.op.inbox }}
      run: |
        python3 -c 'import sys; print(len(sys.stdin.buffer.read()))' < context.txt
outputs:
  result: ${{ sequence.inspect.outputs }}
```

The worker writes `context.txt` before starting the command. The document is not inserted into the shell script, process arguments, or environment. The inbox file is temporary. Ordinary inputs remain ordinary; only explicit artifact sources create files.

`text` requires a string and writes its exact UTF-8 bytes, without an added newline. `json` encodes one JSON value followed by one newline:

```yaml
artifacts:
  context.json:
    kind: value
    format: json
    value: ${{ jq(inputs.flow, 'del(.session)') }}
```

Do not call `json_stringify` first: that would create a JSON string containing JSON text. JSON null, empty strings, arrays, and objects are supported. Omitting `value` is an error. Artifact references inside a JSON value serialize as references; their files are not embedded.

Expressions are evaluated in the normal recipe scope. Text returned by an expression is content, including any literal `${{ ... }}` or shell syntax it contains. Forward a resolved descriptor as a value rather than templating its document text again.

## Names and forwarding

A binding such as `context.json:` supplies the destination filename. Destinations must be relative files; path escapes, directory destinations for individual value sources, and collisions are rejected.

A scalar op input typed as `artifacts.Ref` needs an explicit name:

```yaml
inputs:
  document:
    kind: value
    name: instructions.md
    format: text
    value: Read the attached context before proceeding.
```

A typed `map[string]artifacts.Ref` can use its keys as names. Existing fields typed specifically as `jobdb.ArtifactKey` still require stored artifacts; a local value-backed reference is not a storage key.

Recipe parameters of type `artifact` and `artifact_map` accept value descriptors. Pass a documents map through includes using `${{ inputs.documents }}`, declare `inputs.documents: '${{ inputs.documents }}'` in the included recipe to expose it to its children, and bind it at the consuming op. Artifact-set expansion uses each reference's filename, filling an omitted value-source name from its map key. Ordinary business objects with `kind`, `format`, and `value` fields are not automatically treated as artifacts.

Reuse descriptors through existing inputs, vars, or explicit outputs. There is no new `input_artifacts` context namespace. This feature does not automatically archive generated inbox documents for later story downloads.

## Exporting and human review

Declare result exports explicitly:

```yaml
outputs:
  artifact_refs:
    conversation.json:
      kind: value
      format: json
      value: ${{ states.current.outputs.conversation }}
```

An inline recipe returns value-backed references for subsequent consumers. A standalone job attaches explicitly exported files to its normal job result, using existing artifact persistence. Re-export from the enclosing recipe if an inline helper's document should become a downloadable job result. Conflicts with other output artifact filenames are errors.

Human-review `form.documents` also accepts value sources. Review documents use existing attachment persistence so the client serving the review can open them independently of the task's local inbox. The documents map key can supply the name, or `name` can specify a different filename.

## Limits

Resolved invocations above 128 KiB travel in one attachment through the existing task-input artifact channel. The worker restores the input and writes the inbox files before starting the op. Retries can recreate the files from recorded input; completed-task replay does not rerun the consumer. No separate writer task or new JobDB durability protocol is introduced.

Integration tests cover a 512 KiB document in a native op (including retry and replay after reopening the database), a 2 MiB document passed through an include to a packaged op, and a 512 KiB review document. These are test sizes, not new configured limits.

Existing limits still apply to ordinary job inputs, literal task declarations, and op outputs. Generate large documents from expressions and existing context; embedding a multi-megabyte literal in job inputs or a task declaration can still exceed the application-JSON limit. For a large file that already exists, use the existing stored-artifact upload path.
