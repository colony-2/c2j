package file

// File represents a file to be included in the LLM context
type File struct {
	Path     string                 `json:"path"`
	Name     string                 `json:"name,omitempty"`
	Content  []byte                 `json:"content"`
	MimeType string                 `json:"mime_type"`
	Type     FileType               `json:"type"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// FileType represents the type of file
type FileType string

const (
	FileTypeImage    FileType = "image"
	FileTypePDF      FileType = "pdf"
	FileTypeAudio    FileType = "audio"
	FileTypeVideo    FileType = "video"
	FileTypeUnknown  FileType = "unknown"
	FileTypeText     FileType = "txt"
	FileTypeCode     FileType = "code"     // Source code files
	FileTypeConfig   FileType = "config"   // Configuration files
	FileTypeData     FileType = "data"     // Data files (JSON, XML, CSV)
	FileTypeMarkdown FileType = "markdown" // Documentation
	FileTypeBinary   FileType = "binary"   // Generic binary
)
