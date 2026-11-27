// Package joblabel implements the c2j convention for versioned job labels.
package joblabel

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
)

const prefix = "c2j.label."

type Version = jobdb.MetadataVersion
type Color string
type Projection string
type Status string

const (
	Gray   Color = "gray"
	Red    Color = "red"
	Orange Color = "orange"
	Yellow Color = "yellow"
	Green  Color = "green"
	Blue   Color = "blue"
	Purple Color = "purple"

	Projected         Projection = "projected"
	ProjectionUnknown Projection = "unknown"

	Present   Status = "present"
	Malformed Status = "malformed"
	Tombstone Status = "tombstone"
	Absent    Status = "absent"
	Unknown   Status = "unknown"
)

var (
	ErrInvalidKey     = errors.New("invalid label key")
	ErrInvalidValue   = errors.New("invalid label value")
	ErrInvalidColor   = errors.New("invalid label color")
	ErrInvalidVersion = errors.New("label version must be a canonical nonnegative int64 decimal")
	ErrInvalidEdit    = errors.New("invalid label edit")
	keyPattern        = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
)

type Reader interface {
	GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error)
}

type Label struct {
	Key     string  `json:"key"`
	Value   string  `json:"value"`
	Color   Color   `json:"color"`
	Version Version `json:"version"`
}

type View struct {
	Labels     []Label    `json:"labels"`
	Projection Projection `json:"labels_projection"`
}

type State struct {
	Status  Status   `json:"status"`
	Version *Version `json:"version,omitempty"`
	Label   *Label   `json:"label,omitempty"`
}

type storedLabel struct {
	Value string `json:"value"`
	Color Color  `json:"color"`
}

func (label storedLabel) validate() error {
	if len(label.Value) == 0 || len(label.Value) > 256 || !utf8.ValidString(label.Value) || strings.TrimSpace(label.Value) == "" {
		return ErrInvalidValue
	}
	for _, char := range label.Value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return ErrInvalidValue
		}
	}
	switch label.Color {
	case Gray, Red, Orange, Yellow, Green, Blue, Purple:
		return nil
	default:
		return ErrInvalidColor
	}
}

func ValidateKey(key string) error {
	if !keyPattern.MatchString(key) {
		return ErrInvalidKey
	}
	return nil
}

func ParseVersion(text string) (Version, error) {
	number, err := strconv.ParseInt(text, 10, 64)
	if err != nil || number < 0 || strconv.FormatInt(number, 10) != text {
		return 0, ErrInvalidVersion
	}
	return Version(number), nil
}

func decode(key string, entry jobdb.JobMetadataEntry) State {
	if entry.Version < 0 {
		return State{Status: Malformed}
	}
	state := State{Status: Malformed, Version: &entry.Version}
	if ValidateKey(key) != nil {
		return state
	}
	if !entry.Present {
		state.Status = Tombstone
		return state
	}
	raw := entry.Value
	if clientpayload.ValidateJSONWithLimits(raw, 256<<10, 1) != nil {
		return state
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 || fields["value"] == nil || fields["color"] == nil {
		return state
	}
	var label storedLabel
	if json.Unmarshal(raw, &label) != nil || label.validate() != nil {
		return state
	}
	return State{Status: Present, Version: &entry.Version, Label: &Label{Key: key, Value: label.Value, Color: label.Color, Version: entry.Version}}
}

func FromProjection(projection map[string]jobdb.JobMetadataEntry) View {
	view := View{Projection: ProjectionUnknown, Labels: []Label{}}
	if len(projection) == 0 {
		return view
	}
	view.Projection = Projected
	for name, entry := range projection {
		if key, ok := strings.CutPrefix(name, prefix); ok {
			if label := decode(key, entry).Label; label != nil {
				view.Labels = append(view.Labels, *label)
			}
		}
	}
	sort.Slice(view.Labels, func(i, j int) bool { return view.Labels[i].Key < view.Labels[j].Key })
	return view
}

func ListJobLabels(ctx context.Context, reader Reader, job jobdb.JobKey) (View, error) {
	info, err := reader.GetJob(ctx, job)
	if err != nil {
		return View{}, err
	}
	return FromProjection(info.ExtendedMetadata), nil
}

func GetJobLabel(ctx context.Context, reader Reader, job jobdb.JobKey, key string) (State, error) {
	if err := ValidateKey(key); err != nil {
		return State{}, err
	}
	info, err := reader.GetJob(ctx, job)
	if err != nil {
		return State{}, err
	}
	if len(info.ExtendedMetadata) == 0 {
		return State{Status: Unknown}, nil
	}
	if entry, ok := info.ExtendedMetadata[prefix+key]; ok {
		return decode(key, entry), nil
	}
	return State{Status: Absent, Version: new(Version)}, nil
}
