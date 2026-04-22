# llm_inference2 Op

Runs LLM inference with optional file context and tool execution; returns the model response plus tool/file metadata when used.

## Input Structure

```json
{
  "default_provider": "openai",
  "default_model": "gpt-4.1",
  "api_keys": {
    "openai": "optional"
  },
  "temperature": 0.2,
  "max_tokens": 512,
  "top_p": 1,
  "stop_sequences": [],
  "enable_sandbox": false,
  "allowed_paths": [],
  "restricted_paths": [],
  "default_file_handling": "hybrid",
  "max_file_context_size": 0,
  "prompt": "Summarize the diff.",
  "system_prompt": "Be concise.",
  "response_schema": {},
  "files": [
    {
      "path": "README.md",
      "name": "README.md",
      "content": "SGVsbG8=",
      "mime_type": "text/markdown",
      "type": "markdown",
      "metadata": {}
    }
  ],
  "file_handling": "hybrid",
  "tools": [
    {
      "name": "read_file",
      "description": "Read a file from disk.",
      "parameters": {}
    }
  ],
  "execute_tools": false,
  "default_working_dir": "/path/to/repo",
  "tool_working_dir": "/path/to/repo",
  "enable_tool_execution": false,
  "max_tool_rounds": 0,
  "continue_on_tool_error": false,
  "tool_timeout": "30s",
  "metadata": {}
}
```

## Output Structure

```json
{
  "response": "text or JSON string",
  "model": "gpt-4.1",
  "finish_reason": "stop",
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 200,
    "total_tokens": 300
  },
  "tool_calls": [],
  "tool_results": [],
  "tool_execution_errors": [],
  "tool_rounds_used": 0,
  "files_written": [],
  "files_read": [],
  "files_deleted": [],
  "execution_time_ms": 1234,
  "provider_metadata": {},
  "telemetry": {
    "prompt_tokens": 100,
    "completion_tokens": 200,
    "total_tokens": 300
  }
}
```

### Structured responses

When `response_schema` is provided, the op now enforces the schema and always returns `response` as a **stringified JSON object**. Recipe authors should wrap access with `json_parse`, e.g. `json_parse(sequence.assess_cell.outputs.response).cell_is_appropriate`. The raw model output is validated against the supplied schema (single-element schema arrays are unwrapped) and the op fails with a descriptive error if the shape does not match.
