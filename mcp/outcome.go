package mcp

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/teslashibe/notes"
)

// ErrUncertain marks an application persistence failure with unsafe replay.
var ErrUncertain = errors.New("Notes outcome uncertain")

// Outcome is the JSON data stored in the application's journal and MCP envelope.
type Outcome struct {
	Status             string                `json:"status"`
	NoteID             string                `json:"note_id,omitempty"`
	Title              string                `json:"title,omitempty"`
	FolderID           string                `json:"folder_id,omitempty"`
	ModifiedAt         *time.Time            `json:"modified_at,omitempty"`
	Message            string                `json:"message,omitempty"`
	IsError            bool                  `json:"is_error"`
	ReplayUnsafe       bool                  `json:"automatic_replay_unsafe,omitempty"`
	Notes              []Outcome             `json:"notes,omitempty"`
	Complete           *bool                 `json:"complete,omitempty"`
	Body               *string               `json:"body,omitempty"`
	BodyAvailable      *bool                 `json:"body_available,omitempty"`
	ChecklistAvailable *bool                 `json:"checklist_available,omitempty"`
	Checklist          []notes.ChecklistItem `json:"checklist,omitempty"`
	Text               string                `json:"text,omitempty"`
	Checked            *bool                 `json:"checked,omitempty"`
	Creation           string                `json:"creation,omitempty"`
	Sharing            string                `json:"sharing,omitempty"`
	Items              []Outcome             `json:"items,omitempty"`
	ExpiresAt          *time.Time            `json:"expires_at,omitempty"`
}

func (o Outcome) Encoded() string { data, _ := json.Marshal(o); return string(data) }
func Metadata(n notes.Note) Outcome {
	o := Outcome{NoteID: n.ID, Title: n.Name, FolderID: n.FolderID}
	if !n.ModifiedAt.IsZero() {
		o.ModifiedAt = &n.ModifiedAt
	}
	return o
}
func (o *Outcome) Fail(message string, err error) {
	o.Status = "failed"
	o.IsError = true
	o.Message = message
	var op *notes.OperationError
	if errors.Is(err, ErrUncertain) || (errors.As(err, &op) && op.Uncertain) {
		o.Status = "uncertain"
		o.ReplayUnsafe = true
		o.Message += "; automatic replay is unsafe"
	}
}
