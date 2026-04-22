# sleep Op

Pauses execution for the requested duration; returns timing metadata and whether the sleep was interrupted. Duration parsing follows Go `time.ParseDuration`.

## Input Structure

```json
{
  "duration": "5s"
}
```

## Output Structure

```json
{
  "start_time": "2025-01-02T03:04:05Z",
  "end_time": "2025-01-02T03:04:10Z",
  "actual_duration": "5s",
  "completed": true,
  "interrupted": false,
  "error_message": ""
}
```
