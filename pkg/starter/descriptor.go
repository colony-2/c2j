package starter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/jobsemantics"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/segmentio/ksuid"
	"gopkg.in/yaml.v3"

	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Descriptor is the resolved fresh-submission operation. Persist it before
// publication and replay it unchanged, including metadata and artifact bytes.
// It contains submitted secret references, never resolved credentials.
type Descriptor struct {
	Version   int                  `json:"version"`
	Operation string               `json:"operation"`
	Job       jobdb.SubmitJob      `json:"job"`
	Input     json.RawMessage      `json:"input"`
	Artifacts []DescriptorArtifact `json:"artifacts,omitempty"`
}
type DescriptorArtifact struct {
	Name   string `json:"name"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

func NewDescriptor(ctx context.Context, job jobdb.SubmitJob) (Descriptor, error) {
	d := Descriptor{Version: 2, Operation: "submit", Job: job}
	if job.Data == nil {
		return d, fmt.Errorf("submission input is required")
	}
	raw, err := job.Data.GetData()
	if err != nil {
		return d, err
	}
	d.Input = append(json.RawMessage(nil), raw...)
	if d.Job.JobID == "" {
		d.Job.JobID = ksuid.New().String()
		if job.JobType == RecipeJobType {
			var start workflowctl.StartJob
			if err := jsonexact.Unmarshal(d.Input, &start); err != nil {
				return d, err
			}
			start.JobID = d.Job.JobID
			d.Input, err = json.Marshal(start)
			if err != nil {
				return d, err
			}
		}
	}
	artifacts, err := job.Data.GetArtifacts()
	if err != nil {
		return d, err
	}
	for _, a := range artifacts {
		data, err := a.Bytes(ctx)
		if err != nil {
			return d, err
		}
		sum := sha256.Sum256(data)
		d.Artifacts = append(d.Artifacts, DescriptorArtifact{Name: a.Name(), Data: append([]byte(nil), data...), SHA256: hex.EncodeToString(sum[:])})
	}
	d.Job.Data = nil
	return d, nil
}
func (d Descriptor) Request() (jobdb.SubmitJob, error) {
	job := d.Job
	// encoding/json decodes a RawMessage null into literal bytes. A hash-only
	// selector must remain hash-only after a wire/disk round trip.
	if job.Schema != nil && bytes.Equal(bytes.TrimSpace(job.Schema.Schema), []byte("null")) {
		selector := *job.Schema
		selector.Schema = nil
		job.Schema = &selector
	}
	if (d.Version != 1 && d.Version != 2) || d.Operation != "submit" || !json.Valid(d.Input) || job.Data != nil {
		return job, fmt.Errorf("invalid submission descriptor")
	}
	if job.JobID == "" {
		return job, fmt.Errorf("descriptor requires reserved destination job ID")
	}
	if err := jobdb.ValidatePrimaryGoalSelection(job); err != nil {
		return job, err
	}
	if err := (jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}).Validate(); err != nil {
		return job, err
	}
	if d.Version == 2 {
		if err := d.validateCurrent(job); err != nil {
			return job, err
		}
	}
	artifacts := make([]jobdb.Artifact, 0, len(d.Artifacts))
	for _, a := range d.Artifacts {
		sum := sha256.Sum256(a.Data)
		if a.Name == "" || a.SHA256 != hex.EncodeToString(sum[:]) {
			return job, fmt.Errorf("descriptor artifact integrity: %s", a.Name)
		}
		artifacts = append(artifacts, jobdb.NewArtifactFromBytes(a.Name, a.Data))
	}
	job.Data = &jobdb.SimpleTaskData{Data: append([]byte(nil), d.Input...), Artifacts: artifacts}
	return job, nil
}

// Format 1 is an authoritative historical request, including its interpretation.
// Format 2 must pass current admission even when supplied directly by a caller.
func (d Descriptor) validateCurrent(job jobdb.SubmitJob) error {
	if job.GoalID != "" {
		return fmt.Errorf("goal selection is unsupported")
	}
	for _, dep := range job.Dependencies {
		if dep.Target.GoalID != "" {
			return fmt.Errorf("goal dependencies are unsupported; select an exact job")
		}
	}
	if job.JobType != RecipeJobType {
		return nil
	}
	var start workflowctl.StartJob
	if err := json.Unmarshal(d.Input, &start); err != nil {
		return fmt.Errorf("invalid prepared recipe input: %w", err)
	}
	if start.JobSemanticsVersion != jobsemantics.Current || start.JobContext.JobSemanticsVersion != jobsemantics.Current {
		return fmt.Errorf("format 2 recipe descriptor requires job_semantics_version 2 in input and context")
	}
	if start.TenantId != job.TenantId || start.JobID != job.JobID || start.Cell != job.Cell || start.CellRepo != job.CellRepo {
		return fmt.Errorf("prepared recipe input contradicts destination identity")
	}
	if start.GoalID != "" {
		return fmt.Errorf("goal selection is unsupported")
	}
	for _, dep := range start.Dependencies {
		if dep.Target.GoalID != "" {
			return fmt.Errorf("goal dependencies are unsupported; select an exact job")
		}
	}
	for _, artifact := range d.Artifacts {
		if !strings.HasSuffix(artifact.Name, RecipeArtifactSuffix) {
			continue
		}
		var definition recipe.Recipe
		if err := yaml.Unmarshal(artifact.Data, &definition); err != nil {
			return fmt.Errorf("invalid prepared recipe: %w", err)
		}
		if err := recipe.ValidateJobSemantics(definition, jobsemantics.Current); err != nil {
			return err
		}
	}
	return nil
}
func (d Descriptor) Submit(ctx context.Context, submitter jobref.Submitter) (jobref.Ref, error) {
	job, err := d.Request()
	if err != nil {
		return jobref.Ref{}, err
	}
	if d.Version == 1 && job.GoalID != "" {
		// Replay only the original frozen operation. The destination is its
		// idempotency key; never strip GoalID or rebuild the input.
		var ref jobref.Ref
		var err error
		if recovery, ok := submitter.(jobref.HistoricalRecovery); ok {
			ref, err = recovery.ReplaySubmitJobRef(ctx, job)
		} else {
			ref, err = submitter.SubmitJobRef(ctx, job)
		}
		if err == nil {
			err = ref.ValidateSelected(job)
		}
		return ref, err
	}
	if job.GoalID != "" {
		return jobref.Ref{}, fmt.Errorf("goal selection is unsupported")
	}
	if err := executioncatalog.CheckSubmission(ctx, job); err != nil {
		return jobref.Ref{}, err
	}
	ref, err := submitter.SubmitJobRef(ctx, job)
	if err == nil {
		err = ref.ValidateSubmission(job)
	}
	return ref, err
}
