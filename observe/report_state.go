package observe

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ReportState returns the members of a node report body that a judgement reads: the body's state member when it carries one, and the body whole when it does not.
func ReportState(body json.RawMessage) (json.RawMessage, error) {
	var carried struct {
		State json.RawMessage `json:"state"`
	}
	err := json.Unmarshal(body, &carried)
	var notObject *json.UnmarshalTypeError
	switch {
	case errors.As(err, &notObject):
		// A body that is not an object carries no state member, so its reader fails it as it did before state.
		return body, nil
	case err != nil:
		return nil, err
	case carried.State == nil:
		return body, nil
	case carried.State[0] != '{':
		return nil, fmt.Errorf("the report's state member holds %.40s, which is not an object", carried.State)
	}
	return carried.State, nil
}
