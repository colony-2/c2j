package starter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

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
	d := Descriptor{Version: 1, Operation: "submit", Job: job}
	if job.Data == nil {
		return d, fmt.Errorf("submission input is required")
	}
	raw, err := job.Data.GetData()
	if err != nil {
		return d, err
	}
	d.Input = append(json.RawMessage(nil), raw...)
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
	if d.Version != 1 || d.Operation != "submit" || !json.Valid(d.Input) || job.Data != nil {
		return job, fmt.Errorf("invalid submission descriptor")
	}
	if err := jobdb.ValidatePrimaryGoalSelection(job); err != nil {
		return job, err
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
func (d Descriptor) Submit(ctx context.Context, submitter jobref.Submitter) (jobref.Ref, error) {
	job, err := d.Request()
	if err != nil {
		return jobref.Ref{}, err
	}
	ref, err := submitter.SubmitJobRef(ctx, job)
	if err == nil && job.GoalID != "" {
		err = ref.ValidateSelected(job)
	}
	return ref, err
}
