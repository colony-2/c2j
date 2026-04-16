package funcregistry

// CELCell is the canonical shape returned by the cells() helper.
// Using a defined struct improves schema validation and keeps fields deterministic.
type CELCell struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
}
