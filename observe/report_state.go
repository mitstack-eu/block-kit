package observe

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ReportState returns the members of a node report body that a judgement reads: its member keyed exactly "state" when it carries one, and otherwise the body whole, leaving a body that is not an object to fail in the reader that decodes it.
func ReportState(body json.RawMessage) (json.RawMessage, error) {
	members, err := membersByExactKey(body)
	state, carried := members["state"]
	switch {
	case err != nil:
		return nil, err
	case !carried:
		return body, nil
	case state[0] != '{':
		return nil, fmt.Errorf("the report's state member holds %.40s, which is not an object", state)
	}
	return state, nil
}

func membersByExactKey(body json.RawMessage) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	err := json.Unmarshal(body, &members)
	var notAnObject *json.UnmarshalTypeError
	if errors.As(err, &notAnObject) {
		return nil, nil
	}
	return members, err
}
