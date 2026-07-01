package jobdbschema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

var c2jJobSchema = json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "description": "c2j recipe job chapter schema v1",
  "firstChapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ordinal", "createdAt", "taskType", "body"],
    "properties": {
      "ordinal": { "const": 0 },
      "createdAt": { "type": "string", "format": "date-time" },
      "taskType": { "const": "recipe" },
      "artifacts": {
        "type": "array",
        "items": { "$ref": "#/$defs/storedArtifact" }
      },
      "body": {
        "type": "object",
        "required": ["kind", "input"],
        "properties": {
          "kind": { "const": "jobStart" },
          "input": { "$ref": "#/$defs/startJob" }
        },
        "additionalProperties": false
      }
    },
    "additionalProperties": true,
    "$defs": {
      "artifactKey": {
        "type": "object",
        "required": ["jobId", "taskOrdinal", "name", "sizeBytes"],
        "properties": {
          "jobId": { "type": "string", "minLength": 1 },
          "taskOrdinal": { "type": "integer", "minimum": 0 },
          "name": { "type": "string", "minLength": 1 },
          "sizeBytes": { "type": "integer", "minimum": 0 }
        },
        "additionalProperties": false
      },
      "artifactRef": {
        "type": "object",
        "required": ["kind"],
        "properties": {
          "kind": { "enum": ["stored", "external"] },
          "name": { "type": "string" },
          "stored": {
            "type": "object",
            "required": ["key"],
            "properties": {
              "key": { "$ref": "#/$defs/artifactKey" }
            },
            "additionalProperties": false
          },
          "external": {
            "type": "object",
            "required": ["url"],
            "properties": {
              "url": { "type": "string", "minLength": 1 },
              "expand": { "type": "boolean" }
            },
            "additionalProperties": false
          }
        },
        "additionalProperties": false
      },
      "environmentPathContext": {
        "type": "object",
        "properties": {
          "workdir": { "type": "string" },
          "worktree_path": { "type": "string" },
          "inbox": { "type": "string" },
          "outbox": { "type": "string" }
        },
        "additionalProperties": false
      },
      "environmentContext": {
        "type": "object",
        "properties": {
          "worktree_path": { "type": "string" },
          "workdir": { "type": "string" },
          "inbox": { "type": "string" },
          "outbox": { "type": "string" },
          "host": { "$ref": "#/$defs/environmentPathContext" },
          "op": { "$ref": "#/$defs/environmentPathContext" }
        },
        "additionalProperties": false
      },
      "workflowContext": {
        "type": "object",
        "properties": {
          "cell_id": { "type": "string" },
          "cell": { "type": "string" },
          "job_id": { "type": "string" },
          "project_id": { "type": "string" }
        },
        "additionalProperties": false
      },
      "gitBaseContext": {
        "type": "object",
        "properties": {
          "repo": { "type": "string" },
          "ref": { "type": "string" },
          "resolved_hash": { "type": "string" },
          "author": { "type": "string" }
        },
        "additionalProperties": false
      },
      "recipeSourceContext": {
        "type": "object",
        "properties": {
          "repo": { "type": "string" },
          "ref": { "type": "string" },
          "path": { "type": "string" },
          "selector": { "type": "string" }
        },
        "additionalProperties": false
      },
      "jobContext": {
        "type": "object",
        "properties": {
          "environment": { "$ref": "#/$defs/environmentContext" },
          "artifacts": {
            "type": "object",
            "additionalProperties": { "$ref": "#/$defs/artifactRef" }
          },
          "workflow": { "$ref": "#/$defs/workflowContext" },
          "git": { "$ref": "#/$defs/gitBaseContext" },
          "recipe_source": { "$ref": "#/$defs/recipeSourceContext" }
        },
        "additionalProperties": false
      },
      "parentJobContext": {
        "type": "object",
        "properties": {
          "tenant_id": { "type": "string" },
          "job_id": { "type": "string" },
          "job_type": { "type": "string" },
          "op_type": { "type": "string" },
          "op_step": { "type": "string" },
          "op_task_type": { "type": "string" },
          "cell_name": { "type": "string" },
          "repo": { "type": "string" },
          "git_ref": { "type": "string" },
          "invocation_path": { "type": "string" },
          "invocation_seq": { "type": "integer" },
          "invocation_hash": { "type": "string" }
        },
        "additionalProperties": false
      },
      "startJob": {
        "type": "object",
        "required": ["tenantId", "recipe", "context"],
        "properties": {
          "tenantId": { "type": "string", "minLength": 1 },
          "job_id": { "type": "string" },
          "recipe": { "type": "string", "minLength": 1 },
          "inputs": { "type": "object" },
          "artifact_refs": {
            "type": "array",
            "items": { "$ref": "#/$defs/artifactRef" }
          },
          "context": { "$ref": "#/$defs/jobContext" },
          "parent": { "$ref": "#/$defs/parentJobContext" },
          "git": { "type": "string" },
          "submitted_at": { "type": "string", "format": "date-time" },
          "input_hash": { "type": "string" }
        },
        "additionalProperties": false
      },
      "storedArtifact": {
        "type": "object",
        "required": ["name", "digest", "size"],
        "properties": {
          "name": { "type": "string", "minLength": 1 },
          "digest": { "type": "string", "minLength": 1 },
          "size": { "type": "integer", "minimum": 0 }
        },
        "additionalProperties": true
      }
    }
  },
  "chapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ordinal", "createdAt", "body"],
    "properties": {
      "ordinal": { "type": "integer", "minimum": 1 },
      "createdAt": { "type": "string", "format": "date-time" },
      "taskType": { "type": "string", "minLength": 1 },
      "input": true,
      "artifacts": {
        "type": "array",
        "items": { "$ref": "#/$defs/storedArtifact" }
      },
      "body": true
    },
    "anyOf": [
      { "$ref": "#/$defs/rootResolveChapter" },
      { "$ref": "#/$defs/withinResolveChapter" },
      { "$ref": "#/$defs/activityInvocationChapter" },
      { "$ref": "#/$defs/restartExtraChapter" },
      { "$ref": "#/$defs/jobAttemptOutcomeChapter" }
    ],
    "additionalProperties": true,
    "$defs": {
      "artifactKey": {
        "type": "object",
        "required": ["jobId", "taskOrdinal", "name", "sizeBytes"],
        "properties": {
          "jobId": { "type": "string", "minLength": 1 },
          "taskOrdinal": { "type": "integer", "minimum": 0 },
          "name": { "type": "string", "minLength": 1 },
          "sizeBytes": { "type": "integer", "minimum": 0 }
        },
        "additionalProperties": false
      },
      "artifactRef": {
        "type": "object",
        "required": ["kind"],
        "properties": {
          "kind": { "enum": ["stored", "external"] },
          "name": { "type": "string" },
          "stored": {
            "type": "object",
            "required": ["key"],
            "properties": {
              "key": { "$ref": "#/$defs/artifactKey" }
            },
            "additionalProperties": false
          },
          "external": {
            "type": "object",
            "required": ["url"],
            "properties": {
              "url": { "type": "string", "minLength": 1 },
              "expand": { "type": "boolean" }
            },
            "additionalProperties": false
          }
        },
        "additionalProperties": false
      },
      "inlineBoundaryFrame": {
        "type": "object",
        "properties": {
          "callsite_path": { "type": "string" },
          "boundary_node_path": { "type": "string" },
          "recipe_id": { "type": "string" },
          "recipe_version": { "type": "string" },
          "source_kind": { "type": "string" },
          "submitted_selector": { "type": "string" },
          "resolved_selector": { "type": "string" },
          "resolved_commit": { "type": "string" },
          "content_sha256": { "type": "string" }
        },
        "additionalProperties": false
      },
      "globalGitTaskContext": {
        "type": "object",
        "required": ["invoke_seq"],
        "properties": {
          "base_repo": { "type": "string" },
          "base_ref": { "type": "string" },
          "resolved_base_hash": { "type": "string" },
          "recipe_source_repo": { "type": "string" },
          "recipe_source_ref": { "type": "string" },
          "persist_hash": { "type": "string" },
          "parent_hash": { "type": "string" },
          "cell_name": { "type": "string" },
          "git_author": { "type": "string" },
          "node_path": { "type": "string" },
          "invoke_seq": { "type": "integer", "minimum": 0 },
          "invoke_hash": { "type": "string" },
          "inline_stack": {
            "type": "array",
            "items": { "$ref": "#/$defs/inlineBoundaryFrame" }
          }
        },
        "additionalProperties": false
      },
      "gitCommitContext": {
        "type": "object",
        "properties": {
          "parent_ref": { "type": "string" },
          "hash": { "type": "string" },
          "parent_hash": { "type": "string" }
        },
        "additionalProperties": false
      },
      "startedJobContext": {
        "type": "object",
        "properties": {
          "tenant_id": { "type": "string" },
          "job_id": { "type": "string" },
          "recipe": { "type": "string" },
          "status": { "type": "string" },
          "parent_invocation_hash": { "type": "string" }
        },
        "additionalProperties": false
      },
      "startedJobsContext": {
        "type": "object",
        "properties": {
          "job_ids": {
            "type": "array",
            "items": { "type": "string" }
          },
          "items": {
            "type": "array",
            "items": { "$ref": "#/$defs/startedJobContext" }
          }
        },
        "additionalProperties": false
      },
      "rootSourceResolutionInput": {
        "type": "object",
        "required": ["project_id", "selector"],
        "properties": {
          "project_id": { "type": "string", "minLength": 1 },
          "selector": { "type": "string", "minLength": 1 },
          "lookup_repo": { "type": "string" },
          "lookup_ref": { "type": "string" }
        },
        "additionalProperties": false
      },
      "recipeSourceResolution": {
        "type": "object",
        "required": ["source_kind", "submitted_selector", "was_already_pinned"],
        "properties": {
          "source_kind": { "enum": ["artifact", "serverRef", "git"] },
          "submitted_selector": { "type": "string" },
          "resolved_selector": { "type": "string" },
          "resolved_commit": { "type": "string" },
          "artifact_name": { "type": "string" },
          "was_already_pinned": { "type": "boolean" }
        },
        "additionalProperties": false
      },
      "resolvedRecipeSource": {
        "type": "object",
        "required": ["source_kind", "submitted_selector", "was_already_pinned"],
        "properties": {
          "source_kind": { "enum": ["artifact", "serverRef", "git"] },
          "submitted_selector": { "type": "string" },
          "resolved_selector": { "type": "string" },
          "resolved_commit": { "type": "string" },
          "artifact_name": { "type": "string" },
          "was_already_pinned": { "type": "boolean" },
          "recipe_yaml": { "type": "string" }
        },
        "additionalProperties": false
      },
      "withinRecipeResolutionInput": {
        "type": "object",
        "required": ["selectors"],
        "properties": {
          "selectors": {
            "type": "array",
            "items": { "type": "string", "minLength": 1 }
          },
          "repository_source": { "type": "string" },
          "repository_ref": { "type": "string" },
          "resolved_git_refs": {
            "type": "object",
            "additionalProperties": { "type": "string" }
          }
        },
        "additionalProperties": false
      },
      "withinRecipeResolutionOutput": {
        "type": "object",
        "properties": {
          "resolved_selectors": {
            "type": "object",
            "additionalProperties": { "type": "string" }
          },
          "resolved_git_refs": {
            "type": "object",
            "additionalProperties": { "type": "string" }
          }
        },
        "additionalProperties": false
      },
      "activityInvocationRequest": {
        "type": "object",
        "required": ["input", "context"],
        "properties": {
          "input": {
            "type": "object",
            "additionalProperties": true
          },
          "const": { "type": "boolean" },
          "context": { "$ref": "#/$defs/globalGitTaskContext" },
          "artifact_keys": {
            "type": "array",
            "items": { "$ref": "#/$defs/artifactKey" }
          },
          "artifacts": {
            "type": "object",
            "additionalProperties": { "$ref": "#/$defs/artifactRef" }
          }
        },
        "additionalProperties": false
      },
      "activityInvocationOutput": {
        "type": "object",
        "required": ["output"],
        "properties": {
          "git": { "$ref": "#/$defs/gitCommitContext" },
          "nextTaskType": { "type": "string" },
          "output": true,
          "artifact_refs": {
            "type": "object",
            "additionalProperties": { "$ref": "#/$defs/artifactRef" }
          },
          "jobs": { "$ref": "#/$defs/startedJobsContext" }
        },
        "additionalProperties": false
      },
      "activityOutputEnvelope": {
        "type": "object",
        "required": ["v", "kind", "payload"],
        "properties": {
          "v": { "const": 1 },
          "kind": { "const": "activity_output" },
          "payload": { "$ref": "#/$defs/activityInvocationOutput" }
        },
        "additionalProperties": false
      },
      "scopePatch": {
        "type": "object",
        "required": ["container", "id"],
        "properties": {
          "container": { "enum": ["sequence", "states"] },
          "id": { "type": "string", "minLength": 1 },
          "outputs": {
            "type": "object",
            "additionalProperties": true
          }
        },
        "additionalProperties": false
      },
      "contextPatch": {
        "type": "object",
        "properties": {
          "job": {
            "type": "object",
            "additionalProperties": true
          },
          "scopes": {
            "type": "array",
            "items": { "$ref": "#/$defs/scopePatch" }
          }
        },
        "additionalProperties": false
      },
      "contextPatchEnvelope": {
        "type": "object",
        "required": ["v", "kind", "payload"],
        "properties": {
          "v": { "const": 1 },
          "kind": { "const": "context_patch" },
          "payload": { "$ref": "#/$defs/contextPatch" }
        },
        "additionalProperties": false
      },
      "taskAttemptBody": {
        "type": "object",
        "required": ["kind", "outcome"],
        "properties": {
          "kind": { "const": "taskAttemptOutcome" },
          "outcome": true
        },
        "additionalProperties": false
      },
      "failureOutcome": {
        "type": "object",
        "required": ["kind"],
        "properties": {
          "kind": { "enum": ["appError", "systemError", "timeout"] },
          "error": {
            "type": "object",
            "properties": {
              "message": { "type": "string" },
              "attrs": { "type": "object" }
            },
            "additionalProperties": true
          },
          "timeout": { "type": "string" }
        },
        "additionalProperties": true
      },
      "rootResolveChapter": {
        "type": "object",
        "required": ["taskType", "input", "body"],
        "properties": {
          "taskType": { "const": "recipe_root_source_resolve" },
          "input": { "$ref": "#/$defs/rootSourceResolutionInput" },
          "body": {
            "allOf": [
              { "$ref": "#/$defs/taskAttemptBody" },
              {
                "properties": {
                  "outcome": {
                    "anyOf": [
                      {
                        "type": "object",
                        "required": ["kind", "output"],
                        "properties": {
                          "kind": { "const": "success" },
                          "output": { "$ref": "#/$defs/resolvedRecipeSource" }
                        },
                        "additionalProperties": false
                      },
                      { "$ref": "#/$defs/failureOutcome" }
                    ]
                  }
                }
              }
            ]
          }
        }
      },
      "withinResolveChapter": {
        "type": "object",
        "required": ["taskType", "input", "body"],
        "properties": {
          "taskType": { "const": "recipe_within_resolution" },
          "input": { "$ref": "#/$defs/withinRecipeResolutionInput" },
          "body": {
            "allOf": [
              { "$ref": "#/$defs/taskAttemptBody" },
              {
                "properties": {
                  "outcome": {
                    "anyOf": [
                      {
                        "type": "object",
                        "required": ["kind", "output"],
                        "properties": {
                          "kind": { "const": "success" },
                          "output": { "$ref": "#/$defs/withinRecipeResolutionOutput" }
                        },
                        "additionalProperties": false
                      },
                      { "$ref": "#/$defs/failureOutcome" }
                    ]
                  }
                }
              }
            ]
          }
        }
      },
      "activityInvocationChapter": {
        "type": "object",
        "required": ["taskType", "body"],
        "properties": {
          "taskType": {
            "type": "string",
            "not": { "enum": ["recipe_root_source_resolve", "recipe_within_resolution", "__restart_extra__"] }
          },
          "input": { "$ref": "#/$defs/activityInvocationRequest" },
          "body": {
            "allOf": [
              { "$ref": "#/$defs/taskAttemptBody" },
              {
                "properties": {
                  "outcome": {
                    "anyOf": [
                      {
                        "type": "object",
                        "required": ["kind", "output"],
                        "properties": {
                          "kind": { "const": "success" },
                          "output": { "$ref": "#/$defs/activityOutputEnvelope" }
                        },
                        "additionalProperties": false
                      },
                      { "$ref": "#/$defs/failureOutcome" }
                    ]
                  }
                }
              }
            ]
          }
        }
      },
      "restartExtraChapter": {
        "type": "object",
        "required": ["taskType", "input", "body"],
        "properties": {
          "taskType": { "const": "__restart_extra__" },
          "input": {
            "type": "object",
            "required": ["kind"],
            "properties": {
              "kind": { "const": "context_patch" }
            },
            "additionalProperties": false
          },
          "body": {
            "type": "object",
            "required": ["kind", "output"],
            "properties": {
              "kind": { "const": "restartExtra" },
              "output": { "$ref": "#/$defs/contextPatchEnvelope" }
            },
            "additionalProperties": false
          }
        }
      },
      "jobAttemptOutcomeChapter": {
        "type": "object",
        "required": ["body"],
        "properties": {
          "body": {
            "type": "object",
            "required": ["kind", "outcome"],
            "properties": {
              "kind": { "const": "jobAttemptOutcome" },
              "outcome": {
                "anyOf": [
                  {
                    "type": "object",
                    "required": ["kind", "output"],
                    "properties": {
                      "kind": { "const": "success" },
                      "output": {
                        "type": "object",
                        "additionalProperties": true
                      }
                    },
                    "additionalProperties": false
                  },
                  { "$ref": "#/$defs/failureOutcome" }
                ]
              }
            },
            "additionalProperties": false
          }
        }
      },
      "storedArtifact": {
        "type": "object",
        "required": ["name", "digest", "size"],
        "properties": {
          "name": { "type": "string", "minLength": 1 },
          "digest": { "type": "string", "minLength": 1 },
          "size": { "type": "integer", "minimum": 0 }
        },
        "additionalProperties": true
      }
    }
  },
  "lastChapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ordinal", "createdAt", "body"],
    "properties": {
      "ordinal": { "type": "integer", "minimum": 1 },
      "createdAt": { "type": "string", "format": "date-time" },
      "artifacts": {
        "type": "array",
        "items": { "$ref": "#/$defs/storedArtifact" }
      },
      "body": {
        "type": "object",
        "required": ["kind", "outcome"],
        "properties": {
          "kind": { "const": "jobAttemptOutcome" },
          "outcome": {
            "anyOf": [
              {
                "type": "object",
                "required": ["kind", "output"],
                "properties": {
                  "kind": { "const": "success" },
                  "output": {
                    "type": "object",
                    "additionalProperties": true
                  }
                },
                "additionalProperties": false
              },
              {
                "type": "object",
                "required": ["kind"],
                "properties": {
                  "kind": { "enum": ["appError", "systemError", "timeout"] },
                  "error": {
                    "type": "object",
                    "properties": {
                      "message": { "type": "string" },
                      "attrs": { "type": "object" }
                    },
                    "additionalProperties": true
                  },
                  "timeout": { "type": "string" }
                },
                "additionalProperties": true
              }
            ]
          }
        },
        "additionalProperties": false
      }
    },
    "additionalProperties": true,
    "$defs": {
      "storedArtifact": {
        "type": "object",
        "required": ["name", "digest", "size"],
        "properties": {
          "name": { "type": "string", "minLength": 1 },
          "digest": { "type": "string", "minLength": 1 },
          "size": { "type": "integer", "minimum": 0 }
        },
        "additionalProperties": true
      }
    }
  }
}`)

var (
	hashOnce sync.Once
	hash     string
	hashErr  error
)

type JobSubmitter interface {
	SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error)
}

type RestartSubmitter interface {
	SubmitRestartJob(context.Context, jobdb.SubmitRestartJob) (jobdb.JobKey, error)
}

type Submitter struct {
	JobSubmitter
	RestartSubmitter
	Registry jobdb.JobSchemaRegistry
}

type WorkflowEngine struct {
	jobworkflow.Engine
	Registry jobdb.JobSchemaRegistry
}

func Schema() json.RawMessage {
	return append(json.RawMessage(nil), c2jJobSchema...)
}

func Hash() (string, error) {
	hashOnce.Do(func() {
		hash, _, hashErr = jobdb.JobSchemaHash(c2jJobSchema)
	})
	return hash, hashErr
}

func Selector() (*jobdb.JobSchemaSelector, error) {
	hash, err := Hash()
	if err != nil {
		return nil, err
	}
	return &jobdb.JobSchemaSelector{Hash: hash}, nil
}

func SetSubmitJobSchema(job *jobdb.SubmitJob) error {
	selector, err := Selector()
	if err != nil {
		return err
	}
	job.Schema = selector
	return nil
}

func SetSubmitRestartJobSchema(job *jobdb.SubmitRestartJob) error {
	selector, err := Selector()
	if err != nil {
		return err
	}
	job.Schema = selector
	return nil
}

func Register(ctx context.Context, registry jobdb.JobSchemaRegistry, tenantID string) error {
	if registry == nil {
		return errors.New("register c2j job schema: jobdb schema registry is unavailable")
	}
	_, err := registry.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{
		TenantId: tenantID,
		Schema:   Schema(),
	})
	return err
}

func (s Submitter) SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	if s.JobSubmitter == nil {
		return jobdb.JobKey{}, errors.New("submit c2j job: submitter is nil")
	}
	if err := SetSubmitJobSchema(&job); err != nil {
		return jobdb.JobKey{}, fmt.Errorf("set c2j job schema: %w", err)
	}
	key, err := s.JobSubmitter.SubmitJob(ctx, job)
	if !isJobSchemaNotFound(err) {
		return key, err
	}
	if registerErr := Register(ctx, s.Registry, job.TenantId); registerErr != nil {
		return jobdb.JobKey{}, fmt.Errorf("register c2j job schema after schema miss: %w", registerErr)
	}
	return s.JobSubmitter.SubmitJob(ctx, job)
}

func (s Submitter) SubmitRestartJob(ctx context.Context, job jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	if s.RestartSubmitter == nil {
		return jobdb.JobKey{}, errors.New("submit c2j restart job: submitter is nil")
	}
	if err := SetSubmitRestartJobSchema(&job); err != nil {
		return jobdb.JobKey{}, fmt.Errorf("set c2j job schema: %w", err)
	}
	key, err := s.RestartSubmitter.SubmitRestartJob(ctx, job)
	if !isJobSchemaNotFound(err) {
		return key, err
	}
	if registerErr := Register(ctx, s.Registry, job.PriorJobKey.TenantId); registerErr != nil {
		return jobdb.JobKey{}, fmt.Errorf("register c2j job schema after schema miss: %w", registerErr)
	}
	return s.RestartSubmitter.SubmitRestartJob(ctx, job)
}

func (e WorkflowEngine) SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	if job.JobType != "recipe" {
		return e.Engine.SubmitJob(ctx, job)
	}
	return Submitter{
		JobSubmitter: e.Engine,
		Registry:     e.Registry,
	}.SubmitJob(ctx, job)
}

func (e WorkflowEngine) SubmitRestartJob(ctx context.Context, job jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	if job.Schema == nil {
		return e.Engine.SubmitRestartJob(ctx, job)
	}
	return Submitter{
		RestartSubmitter: e.Engine,
		Registry:         e.Registry,
	}.SubmitRestartJob(ctx, job)
}

func isJobSchemaNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, jobdb.ErrJobSchemaNotFound) {
		return true
	}
	return strings.Contains(err.Error(), jobdb.ErrJobSchemaNotFound.Error())
}
