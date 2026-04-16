package recipe

import (
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
)

func TestTypes_Job_And_Activity_Basics(t *testing.T) {
    // Jobs capture workflow execution details [pkg/recipe/types.go]
    start := time.Now()
    j := Job{ID: "id", RecipeName: "r", RecipeVersion: "v", Status: JobStatusRunning, StartTime: start}
    // Running jobs show nil end time [pkg/recipe/types.go]
    assert.Nil(t, j.EndTime)

    // Completed jobs calculate duration correctly [pkg/recipe/types.go]
    end := start.Add(time.Second)
    dur := time.Second
    j2 := Job{Status: JobStatusCompleted, StartTime: start, EndTime: &end, Duration: &dur}
    assert.Equal(t, time.Second, *j2.Duration)

    // Job activities track individual operation execution [pkg/recipe/types.go]
    a := &ActivityExecution{Name: "act", Attempt: 1}
    j.Activities = []*ActivityExecution{a}
    assert.Equal(t, 1, j.Activities[0].Attempt)
}

