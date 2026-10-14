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
  "description": "c2j recipe execution chapter schema v2",
  "firstChapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": [
      "ordinal",
      "createdAt",
      "taskType",
      "body"
    ],
    "properties": {
      "ordinal": {
        "const": 0
      },
      "createdAt": {
        "type": "string",
        "format": "date-time"
      },
      "taskType": {
        "const": "recipe"
      },
      "artifacts": {
        "type": "array",
        "items": {
          "$ref": "#/$defs/storedArtifact"
        }
      },
      "body": {
        "type": "object",
        "required": [
          "kind",
          "input"
        ],
        "properties": {
          "kind": {
            "const": "jobStart"
          },
          "input": {
            "$ref": "#/$defs/startJob"
          }
        },
        "additionalProperties": false
      }
    },
    "additionalProperties": true,
    "$defs": {
      "workspaceContext": {
        "type": "object",
        "properties": {
          "cell": {
            "type": "string"
          },
          "scope_id": {
            "type": "string"
          },
          "parent_scope_id": {
            "type": "string"
          },
          "initial_hash": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "cellResolutionContext": {
        "type": "object",
        "properties": {
          "pattern": {
            "type": "string"
          },
          "self_repo": {
            "type": "string"
          },
          "self_ref": {
            "type": "string"
          },
          "root_repo": {
            "type": "string"
          },
          "root_ref": {
            "type": "string"
          },
          "base_dir": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "artifactKey": {
        "type": "object",
        "required": [
          "jobId",
          "taskOrdinal",
          "name",
          "sizeBytes"
        ],
        "properties": {
          "jobId": {
            "type": "string",
            "minLength": 1
          },
          "taskOrdinal": {
            "type": "integer",
            "minimum": 0
          },
          "name": {
            "type": "string",
            "minLength": 1
          },
          "sizeBytes": {
            "type": "integer",
            "minimum": 0
          }
        },
        "additionalProperties": false
      },
      "artifactRef": {
        "type": "object",
        "required": [
          "kind"
        ],
        "properties": {
          "kind": {
            "enum": [
              "stored",
              "external"
            ]
          },
          "name": {
            "type": "string"
          },
          "stored": {
            "type": "object",
            "required": [
              "key"
            ],
            "properties": {
              "key": {
                "$ref": "#/$defs/artifactKey"
              }
            },
            "additionalProperties": false
          },
          "external": {
            "type": "object",
            "required": [
              "url"
            ],
            "properties": {
              "url": {
                "type": "string",
                "minLength": 1
              },
              "expand": {
                "type": "boolean"
              }
            },
            "additionalProperties": false
          }
        },
        "additionalProperties": false
      },
      "environmentPathContext": {
        "type": "object",
        "properties": {
          "workdir": {
            "type": "string"
          },
          "worktree_path": {
            "type": "string"
          },
          "inbox": {
            "type": "string"
          },
          "outbox": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "environmentContext": {
        "type": "object",
        "properties": {
          "worktree_path": {
            "type": "string"
          },
          "workdir": {
            "type": "string"
          },
          "inbox": {
            "type": "string"
          },
          "outbox": {
            "type": "string"
          },
          "host": {
            "$ref": "#/$defs/environmentPathContext"
          },
          "op": {
            "$ref": "#/$defs/environmentPathContext"
          }
        },
        "additionalProperties": false
      },
      "workflowContext": {
        "type": "object",
        "properties": {
          "cell_id": {
            "type": "string"
          },
          "cell": {
            "type": "string"
          },
          "job_id": {
            "type": "string"
          },
          "project_id": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "gitBaseContext": {
        "type": "object",
        "properties": {
          "repo": {
            "type": "string"
          },
          "ref": {
            "type": "string"
          },
          "resolved_hash": {
            "type": "string"
          },
          "author": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "recipeSourceContext": {
        "type": "object",
        "properties": {
          "repo": {
            "type": "string"
          },
          "ref": {
            "type": "string"
          },
          "path": {
            "type": "string"
          },
          "selector": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "jobContext": {
        "type": "object",
        "properties": {
          "workspace": {
            "$ref": "#/$defs/workspaceContext"
          },
          "cell_resolution": {
            "$ref": "#/$defs/cellResolutionContext"
          },
          "initial_commit": {
            "type": "object",
            "properties": {
              "hash": {
                "type": "string"
              },
              "parent_hash": {
                "type": "string"
              },
              "parent_ref": {
                "type": "string"
              }
            },
            "additionalProperties": false
          },
          "restore_artifact": {
            "$ref": "#/$defs/artifactKey"
          },
          "environment": {
            "$ref": "#/$defs/environmentContext"
          },
          "artifacts": {
            "type": "object",
            "additionalProperties": {
              "$ref": "#/$defs/artifactRef"
            }
          },
          "workflow": {
            "$ref": "#/$defs/workflowContext"
          },
          "git": {
            "$ref": "#/$defs/gitBaseContext"
          },
          "recipe_source": {
            "$ref": "#/$defs/recipeSourceContext"
          }
        },
        "additionalProperties": false
      },
      "parentJobContext": {
        "type": "object",
        "properties": {
          "tenant_id": {
            "type": "string"
          },
          "job_id": {
            "type": "string"
          },
          "job_type": {
            "type": "string"
          },
          "op_type": {
            "type": "string"
          },
          "op_step": {
            "type": "string"
          },
          "op_task_type": {
            "type": "string"
          },
          "cell_name": {
            "type": "string"
          },
          "repo": {
            "type": "string"
          },
          "git_ref": {
            "type": "string"
          },
          "invocation_path": {
            "type": "string"
          },
          "invocation_seq": {
            "type": "integer"
          },
          "invocation_hash": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "startJob": {
        "type": "object",
        "required": [
          "tenantId",
          "recipe",
          "context"
        ],
        "properties": {
          "execution": {
            "type": "object"
          },
          "tenantId": {
            "type": "string",
            "minLength": 1
          },
          "job_id": {
            "type": "string"
          },
          "recipe": {
            "type": "string",
            "minLength": 1
          },
          "inputs": {
            "type": "object"
          },
          "artifact_refs": {
            "type": "array",
            "items": {
              "$ref": "#/$defs/artifactRef"
            }
          },
          "context": {
            "$ref": "#/$defs/jobContext"
          },
          "parent": {
            "$ref": "#/$defs/parentJobContext"
          },
          "git": {
            "type": "string"
          },
          "submitted_at": {
            "type": "string",
            "format": "date-time"
          },
          "input_hash": {
            "type": "string"
          }
        },
        "additionalProperties": false
      },
      "storedArtifact": {
        "type": "object",
        "required": [
          "name",
          "digest",
          "size"
        ],
        "properties": {
          "name": {
            "type": "string",
            "minLength": 1
          },
          "digest": {
            "type": "string",
            "minLength": 1
          },
          "size": {
            "type": "integer",
            "minimum": 0
          }
        },
        "additionalProperties": true
      }
    }
  },
  "chapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": [
      "ordinal",
      "createdAt",
      "body"
    ],
    "properties": {
      "ordinal": {
        "type": "integer",
        "minimum": 1
      },
      "createdAt": {
        "type": "string",
        "format": "date-time"
      },
      "taskType": {
        "type": "string",
        "minLength": 1
      },
      "input": true,
      "artifacts": {
        "type": "array",
        "items": {
          "$ref": "#/$defs/storedArtifact"
        }
      },
      "body": true
    },
    "anyOf": [
      {
        "properties": {
          "body": {
            "type": "object",
            "required": [
              "kind",
              "record"
            ],
            "properties": {
              "kind": {
                "const": "execution"
              },
              "record": {
                "type": "object"
              }
            },
            "additionalProperties": false
          }
        }
      },
      {
        "$ref": "#/$defs/jobAttemptOutcomeChapter"
      }
    ],
    "additionalProperties": true,
    "$defs": {
      "failureOutcome": {
        "type": "object",
        "required": [
          "kind"
        ],
        "properties": {
          "kind": {
            "enum": [
              "appError",
              "systemError",
              "timeout"
            ]
          },
          "error": {
            "type": "object",
            "properties": {
              "message": {
                "type": "string"
              },
              "attrs": {
                "type": "object"
              }
            },
            "additionalProperties": true
          },
          "timeout": {
            "type": "object",
            "required": [
              "scope",
              "after",
              "retryable"
            ],
            "properties": {
              "scope": {
                "enum": [
                  "invocation",
                  "total"
                ]
              },
              "after": {
                "type": "string"
              },
              "retryable": {
                "type": "boolean"
              },
              "input_ref": {
                "type": "object"
              },
              "kind": {
                "type": "string"
              },
              "component": {
                "type": "string"
              },
              "code": {
                "type": "string"
              },
              "message": {
                "type": "string"
              }
            },
            "additionalProperties": true
          }
        },
        "additionalProperties": true
      },
      "jobAttemptOutcomeChapter": {
        "type": "object",
        "required": [
          "body"
        ],
        "properties": {
          "body": {
            "type": "object",
            "required": [
              "kind",
              "outcome"
            ],
            "properties": {
              "kind": {
                "const": "jobAttemptOutcome"
              },
              "outcome": {
                "anyOf": [
                  {
                    "type": "object",
                    "required": [
                      "kind",
                      "output"
                    ],
                    "properties": {
                      "kind": {
                        "const": "success"
                      },
                      "output": {
                        "type": "object",
                        "additionalProperties": true
                      }
                    },
                    "additionalProperties": false
                  },
                  {
                    "$ref": "#/$defs/failureOutcome"
                  }
                ]
              }
            },
            "additionalProperties": false
          }
        }
      },
      "storedArtifact": {
        "type": "object",
        "required": [
          "name",
          "digest",
          "size"
        ],
        "properties": {
          "name": {
            "type": "string",
            "minLength": 1
          },
          "digest": {
            "type": "string",
            "minLength": 1
          },
          "size": {
            "type": "integer",
            "minimum": 0
          }
        },
        "additionalProperties": true
      }
    }
  },
  "lastChapterShape": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": [
      "ordinal",
      "createdAt",
      "body"
    ],
    "properties": {
      "ordinal": {
        "type": "integer",
        "minimum": 1
      },
      "createdAt": {
        "type": "string",
        "format": "date-time"
      },
      "artifacts": {
        "type": "array",
        "items": {
          "$ref": "#/$defs/storedArtifact"
        }
      },
      "body": {
        "type": "object",
        "required": [
          "kind",
          "outcome"
        ],
        "properties": {
          "kind": {
            "const": "jobAttemptOutcome"
          },
          "outcome": {
            "anyOf": [
              {
                "type": "object",
                "required": [
                  "kind",
                  "output"
                ],
                "properties": {
                  "kind": {
                    "const": "success"
                  },
                  "output": {
                    "type": "object",
                    "additionalProperties": true
                  }
                },
                "additionalProperties": false
              },
              {
                "type": "object",
                "required": [
                  "kind"
                ],
                "properties": {
                  "kind": {
                    "enum": [
                      "appError",
                      "systemError",
                      "timeout"
                    ]
                  },
                  "error": {
                    "type": "object",
                    "properties": {
                      "message": {
                        "type": "string"
                      },
                      "attrs": {
                        "type": "object"
                      }
                    },
                    "additionalProperties": true
                  },
                  "timeout": {
                    "type": "object",
                    "required": [
                      "scope",
                      "after",
                      "retryable"
                    ],
                    "properties": {
                      "scope": {
                        "enum": [
                          "invocation",
                          "total"
                        ]
                      },
                      "after": {
                        "type": "string"
                      },
                      "retryable": {
                        "type": "boolean"
                      },
                      "input_ref": {
                        "type": "object"
                      },
                      "kind": {
                        "type": "string"
                      },
                      "component": {
                        "type": "string"
                      },
                      "code": {
                        "type": "string"
                      },
                      "message": {
                        "type": "string"
                      }
                    },
                    "additionalProperties": true
                  }
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
        "required": [
          "name",
          "digest",
          "size"
        ],
        "properties": {
          "name": {
            "type": "string",
            "minLength": 1
          },
          "digest": {
            "type": "string",
            "minLength": 1
          },
          "size": {
            "type": "integer",
            "minimum": 0
          }
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

func (s Submitter) GetJob(ctx context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	reader, ok := s.RestartSubmitter.(interface {
		GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error)
	})
	if !ok {
		return jobdb.JobInfo{}, errors.New("restart submitter does not support reading prior execution state")
	}
	return reader.GetJob(ctx, key)
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
