package childbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"net/http"
)

var selectionErrors = []error{
	jobdb.ErrPrimaryGoalSelectionUnsupported, jobdb.ErrPrimaryGoalSelectionForbidden,
	jobdb.ErrSubmissionReceiptUnavailable, jobdb.ErrGoalNotFound, jobdb.ErrGoalConflict,
	jobdb.ErrExistingJobMismatch, jobdb.ErrConflict, jobdb.ErrLeasePermissionDenied,
	jobdb.ErrInvalidDependency, jobdb.ErrCellNotFound, jobdb.ErrCellConflict, jobdb.ErrCellForbidden, jobdb.ErrCellRepoMismatch, jobdb.ErrCellRetired,
}

func writeSelectionError(w http.ResponseWriter, err error) {
	code := ""
	for _, known := range selectionErrors {
		if errors.Is(err, known) {
			code = known.Error()
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": err.Error()})
}
func decodeSelectionError(status int, msg string, selected bool) error {
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(msg), &body) == nil {
		for _, known := range selectionErrors {
			if body.Code == known.Error() {
				return fmt.Errorf("%w: %s", known, body.Message)
			}
		}
	}
	if selected && (status == 404 || status == 405 || status == 501 || status >= 300 && status < 400) {
		return jobdb.ErrPrimaryGoalSelectionUnsupported
	}
	return fmt.Errorf("child job broker rejected submit: %s", msg)
}
