# Task Execution Context Reference for Recipe Authors

Use the `context` object in templates to access task execution metadata (who/what/where is running, ticket info, repo state, and invocation details). This is a user-facing map of every available field and how to reference it.

## Fields You Can Use

### Actor (who triggered the task)
- `context.actor.ticket_id` - The ticket ID that triggered the job.
- `context.actor.actor_name` - Display name of the actor.
- `context.actor.actor_email` - Email of the actor.

### Ticket (if the task is tied to a ticket)
- `context.ticket.id` - Ticket ID.
- `context.ticket.title` - Ticket title.
- `context.ticket.description` - Ticket description/body.
- `context.ticket.creator.type` - Creator type (for example, user or agent).
- `context.ticket.creator.user.email` - Creator user email (when type indicates a user).
- `context.ticket.creator.agent.cell` - Creator agent cell name.
- `context.ticket.creator.agent.workflow_name` - Creator agent workflow name.
- `context.ticket.creator.agent.execution_id` - Creator agent execution ID.
- `context.ticket.creator.agent.invocation_hash` - Creator agent invocation hash.
- `context.ticket.created_at` - Ticket creation timestamp.
- `context.ticket.updated_at` - Ticket update timestamp.

### Environment (where files live during the run)
- `context.environment.worktree_path` - Worktree root path.
- `context.environment.workdir` - Work directory path.
- `context.environment.inbox` - Artifact inbox directory path.
- `context.environment.outbox` - Artifact outbox directory path.

### Workflow (which job/cell is executing)
- `context.workflow.cell` - Cell name.
- `context.workflow.cell_path` - Cell path relative to repo root.
- `context.workflow.job_id` - Job identifier.
- `context.workflow.project_id` - Project identifier.

### Git task (what repo/revision the task started from and what it produced)
- `context.git.repo` - Base repo identifier or URL.
- `context.git.ref` - Base ref (branch/tag/etc).
- `context.git.resolved_hash` - Resolved base commit hash.
- `context.git.author` - Git author string for generated commits.
- `context.git.parent_ref` - Ref carrying workspace state until a hash exists.
- `context.git.hash` - Persisted commit hash once materialized.
- `context.git.parent_hash` - Parent commit hash once materialized.

### Invocation (task-specific execution info)
- `context.invocation.hash` - Invocation hash. (a deterministic hash of the node path and invocation sequence)
- `context.invocation.path` - Node path within the recipe.
- `context.invocation.sequence` - Invocation sequence number.

## Notes for Template Authors
- All fields are optional; if a value is missing, the template resolves to empty.
- Environment paths may show placeholder values during compile-time validation; the real paths are substituted at execution time.

## Common Examples
```yaml
# Write into the artifact outbox directory
working_directory: "{{ context.environment.outbox }}"

# Use the repo worktree in an op default
default_working_dir: "{{ context.environment.worktree_path }}"

# Pass the cell path to a command
run: "echo {{ context.workflow.cell_path }}"

# Resolve the cell path for op defaults
cell_relative_path: "{{ context.workflow.cell_path }}"

# Use task invocation info for namespacing
artifact_key: "{{ context.invocation.hash }}"
```
