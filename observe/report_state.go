package observe

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ReportState returns the members of a node report body that a judgement reads: the body's state member when it carries one, and the body whole when it does not.
func ReportState(body json.RawMessage) (json.RawMessage, error) {
	// A map matches the key exactly, where a struct field would also take a top-level State as the state member.
	var members map[string]json.RawMessage
	err := json.Unmarshal(body, &members)
	state, carried := members["state"]
	var notObject *json.UnmarshalTypeError
	switch {
	case errors.As(err, &notObject):
		// A body that is not an object has no state member, so the reader decoding it whole fails it.
		return body, nil
	case err != nil:
		return nil, err
	case !carried:
		return body, nil
	case state[0] != '{':
		return nil, fmt.Errorf("the report's state member holds %.40s, which is not an object", state)
	}
	return state, nil
}
