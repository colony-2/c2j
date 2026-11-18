package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/stretchr/testify/require"
)

type metricInspection struct{ query jobinspect.Request }

func (p *metricInspection) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	p.query = q
	snapshot := metrics.Empty(45)
	snapshot.Series["tokens_total"] = []metrics.Point{{Descriptor: metrics.Descriptor{Name: "tokens_total", Kind: "counter", NumberType: "int64", Unit: "{token}"}, Value: int64(9007199254740993)}}
	return &jobinspect.Response{Version: 1, Metrics: &metrics.Result{Snapshot: snapshot}}, nil
}
func (*metricInspection) Download(context.Context, jobinspect.Request) (io.ReadCloser, error) {
	panic("unexpected download")
}
func TestJobMetricsTextJSONAndFile(t *testing.T) {
	for _, mode := range []string{"text", "json", "file"} {
		t.Run(mode, func(t *testing.T) {
			uri, embed, jsonOutput := "", false, mode == "json"
			api := &metricInspection{}
			command := newJobMetricsCmd(&uri, &embed, &jsonOutput, func(context.Context, string) (jobinspect.API, func(), string, error) {
				return api, func() {}, "current-job", nil
			})
			var buf bytes.Buffer
			command.SetOut(&buf)
			args := []string{"self", "--through-ordinal", "45", "--name", "tokens_total"}
			file := filepath.Join(t.TempDir(), "metrics.json")
			if mode == "file" {
				args = append(args, "--output", file)
			}
			command.SetArgs(args)
			require.NoError(t, command.Execute())
			require.Equal(t, "current-job", api.query.JobID)
			require.Equal(t, int64(45), *api.query.Metrics.ThroughOrdinal)
			raw := buf.Bytes()
			if mode == "file" {
				var e error
				raw, e = os.ReadFile(file)
				require.NoError(t, e)
			}
			require.Contains(t, string(raw), "9007199254740993")
			if mode == "text" {
				require.Contains(t, string(raw), "no measured interval")
			} else {
				var r metrics.Result
				require.NoError(t, json.Unmarshal(raw, &r))
				require.Equal(t, int64(9007199254740993), r.Snapshot.Series["tokens_total"][0].Value)
			}
		})
	}
}
func TestJobMetricsInvalidSelectorsFailBeforeOpening(t *testing.T) {
	for _, args := range [][]string{{"JOB", "--attempt", "0"}, {"JOB", "--attempt", "2"}, {"JOB", "--scope", "name"}, {"JOB", "--through-ordinal", "-1"}} {
		uri, embed, j := "", false, false
		c := newJobMetricsCmd(&uri, &embed, &j, func(context.Context, string) (jobinspect.API, func(), string, error) {
			t.Fatal("invalid request opened runtime")
			return nil, nil, "", nil
		})
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		c.SetArgs(args)
		require.Error(t, c.Execute())
	}
}
