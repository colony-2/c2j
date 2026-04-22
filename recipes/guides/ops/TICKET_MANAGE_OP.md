# ticket.manage Op

This document describes how to configure and use the `ticket.manage` recipe op, including all supported input and output shapes.

## Overview

`ticket.manage` executes a list of ticket Actions in order, inside a single database transaction. All actions must succeed or the whole batch fails.

### Child Tickets And Child Recipes

Creating a ticket via `ticket.manage` can automatically start the ticket recipe for that new ticket when the recipe engine is configured. If the creator is an agent (i.e., a running recipe sets `actor.type = "agent"`), the new ticket is treated as a child ticket from the parent workflow perspective: the actor metadata (cell/workflow/execution/invocation) is recorded on the ticket and is passed into the child recipe context. The child recipe starts using the default ticket recipe resolution (cell default, then project default, then `internal://new_ticket`) and a `workflow` event with the running job ID is appended to the new ticket.

## Input Shape

The op accepts a single input object:

```json
{
  "actions": [
    {
      "type": "create_ticket",
      "cell": "api",
      "project_id": "proj_123",
      "title": "Autostart",
      "stage": "open",
      "state": "waiting_user",
      "description": "optional text",
      "actor": {
        "type": "user",
        "user": {
          "email": "owner@example.com"
        }
      }
    }
  ]
}
```

### Common Fields

Every action must include:

- `type` (string, required): One of the Action types listed below.
- `actor` (object, optional): Overrides the fallback actor supplied by the workflow context.

Actor payload:

```json
{
  "type": "user",
  "user": {
    "email": "owner@example.com"
  }
}
```

```json
{
  "type": "agent",
  "agent": {
    "cell": "api",
    "workflow_name": "recipe-name",
    "execution_id": "exec-123",
    "invocation_hash": "inv-456"
  }
}
```

### Action Types

#### create_ticket

Fields:
- `cell` (string, required)
- `project_id` (string, required)
- `title` (string, required)
- `stage` (string, required)
- `state` (string, required)
- `description` (string, optional)
- `actor` (object, optional)

Notes:
- `ticket_id` must not be provided.
- When a recipe is auto-started for the ticket, the create result includes `job_id`.
- Child tickets are created the same way as any other ticket: if the action actor is an agent, the ticket's creator is recorded with that agent metadata, and the child recipe starts automatically when the engine/recipe provider are available.

#### update_ticket

Fields:
- `ticket_id` (string, required)
- `expected_version` (int, optional, must be > 0)
- `stage` (string, optional)
- `state` (string, optional)
- `description` (string, optional)
- `actor` (object, optional) - patches the ticket creator

Notes:
- At least one of `stage`, `state`, `description`, `actor` is required.

#### append_ticket_note

Fields:
- `ticket_id` (string, required)
- `note` (string, required)
- `event_time` (string timestamp, optional)
- `actor` (object, optional)

#### link_markdown_doc

Fields:
- `ticket_id` (string, required)
- `name` (string, required)
- `path` (string, required)
- `reason` (string, optional)
- `event_time` (string timestamp, optional)
- `actor` (object, optional)

#### override_markdown_doc

Same fields as `link_markdown_doc`.

#### remove_markdown_doc

Same fields as `link_markdown_doc`.

#### append_workflow_event

Fields:
- `ticket_id` (string, required)
- `workflow_id` (string, required)
- `run_id` (string, required)
- `status` (string, required): `running`, `completed`, or `failed`
- `event_time` (string timestamp, optional)
- `actor` (object, optional)

#### reset_ticket

Fields:
- `ticket_id` (string, required)
- `reason` (string, required)
- `anchor_event_id` (string, optional)
- `actor` (object, optional)

Notes:
- When `anchor_event_id` is omitted, all non-reset events are reset.

## Output Shape

The op returns a list of results in the same order as the input actions:

```json
{
  "results": [
    {
      "ticket": { "...": "ticket fields" },
      "job_id": "job-123"
    },
    {
      "ticket": { "...": "ticket fields" }
    }
  ]
}
```

### Result Types

#### create_ticket result

```json
{
  "ticket": { "...": "ticket fields" },
  "job_id": "job-123"
}
```

#### update_ticket result

```json
{
  "ticket": { "...": "ticket fields" }
}
```

#### append_ticket_note result

```json
{
  "event": { "...": "ticket event fields" }
}
```

#### markdown results (link/override/remove)

```json
{
  "event": { "...": "ticket event fields" }
}
```

#### append_workflow_event result

```json
{
  "event": { "...": "ticket event fields" }
}
```

#### reset_ticket result

```json
{
  "reset": { "...": "reset fields" },
  "ticket": { "...": "ticket fields" }
}
```

## Recipe YAML Example

```yaml
id: internal://new_ticket
version: "0.0.1"
op: ticket.manage
inputs:
  actions:
    - type: update_ticket
      ticket_id: "{{ context.actor.ticket_id }}"
      expected_version: 1
      stage: "completed"
      state: "waiting_user"
```
