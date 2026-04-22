# c2 CLI – Quick User Guide

## Common Patterns
- Tables by default; JSON with `--output json`.
- IDs printed on create when not using JSON.
- Timeouts are per-command; set via `--timeout 30s`.

## Commands

### Projects
- List: `c2 project list [--id ID --name NAME --name-contains SUB]`
- Create: `c2 project create --name NAME --git-repo-path /path/to/repo`
- Update: `c2 project update <project-id> [--name NAME --git-repo-path PATH --git-repo-branch BRANCH --default-ticket-recipe RECIPE | --clear-default-ticket-recipe]`
- Delete: `c2 project delete <project-id>`

### Tickets
- List: `c2 ticket list [--state working --stage triage --cell CELL]`
- Create: `c2 ticket create --title TITLE --cell CELL --state working --stage triage [--description TEXT]`

### Recipes
- List: `c2 recipe list [--status all|published|unpublished]`
- Get content: `c2 recipe get <name> [--ref COMMIT]` (JSON: full recipe; table: raw YAML)
- Info/history: `c2 recipe info <name>` (shows revisions; marks published)
- Create: `c2 recipe create --name NAME (--content FILE_CONTENT | --content-file PATH) [--publish]`
- Update: `c2 recipe update <name> (--content FILE_CONTENT | --content-file PATH) [--publish]`
- Validate (no write): `c2 recipe validate --name NAME (--content FILE_CONTENT | --content-file PATH)`

### Workflows
- List: `c2 workflow list [--status running --ticket-id TID --cell-id CID --since RFC3339 --until RFC3339 --limit N --offset N]`
- Run: `c2 workflow run --recipe NAME --cell-id CELL [--ticket-id TID --git-ref REF --actor-email EMAIL --idempotency-key KEY --input k=v ...]`
- Output: `c2 workflow output [get] <workflow-id> [--chapter N]` (defaults to last chapter with output; fails fast if workflow status is failed/terminated/timed_out/canceled/unknown)
- Outcome (single API call): `c2 workflow outcome <workflow-id>`
- Artifacts: `c2 workflow artifact list <workflow-id>`; download: `c2 workflow artifact get <workflow-id> --chapter N --name NAME [--output-file PATH]`
- Story: `c2 workflow story <workflow-id>`

### Input Requests
- List pending: `c2 input-request list`
- Respond: `c2 input-request respond <job-id> [--field key=val ...] [--response value]`

### Cells
- List: `c2 cell list [--name NAME]`
- Create: `c2 cell create --name NAME --working-path PATH [--description TEXT --populator NAME --populator-id ID]`
- Update: `c2 cell update <cell-id> [--name NAME --working-path PATH --description TEXT]`

## Tips
- Use `--trace` to print HTTP method/URL/status for troubleshooting.
- Combine with `--output json` for scripting.
- When updates take no flags, the CLI will refuse to send an empty payload.
