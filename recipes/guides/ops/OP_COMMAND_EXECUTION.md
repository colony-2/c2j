# command_execution Op

Executes a shell command with optional working directory, env, shell, and timeout; captures stdout/stderr and exit status.

## Input Structure

```json
{
  "run": "npm test",
  "working_directory": "/path/to/repo",
  "shell": "bash",
  "env": {
    "CI": "true"
  },
  "continue_on_error": false,
  "timeout": "5m"
}
```

## Output Structure

```json
{
  "stdout": "test output",
  "stderr": "",
  "exit_code": 0,
  "success": true,
  "timed_out": false,
  "error_message": ""
}
```
