package cloud

import (
	"cmp"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const serverTypeParam = "serverType"

// ServerTypes is a block's serverType input: the types it runs on, the most entries it takes, where 0 takes as many as Allowed holds, and the list read when a request gives none, where "" refuses that request.
type ServerTypes struct {
	Allowed []string
	Max     int
	Default string
}

// Read returns the request's serverType entries in order, and refuses as invalid-param an entry past Max, an entry outside Allowed, or a repeat.
func (s ServerTypes) Read(d contract.Desired) ([]string, error) {
	value := cmp.Or(d.Params[serverTypeParam], s.Default)
	allowed := strings.Join(s.Allowed, ", ")
	if value == "" {
		return nil, handler.Refuse("invalid-param", `params[%q] is empty; the block runs on %s`, serverTypeParam, allowed)
	}
	entries := strings.Split(value, ",")
	for i := range entries {
		entry := strings.TrimSpace(entries[i])
		entries[i] = entry
		switch {
		case s.Max > 0 && i >= s.Max:
			return nil, handler.Refuse("invalid-param", `params[%q] is %q; the block takes at most %d %s, so its entry %q is one too many`,
				serverTypeParam, value, s.Max, plural(s.Max, "type"), entry)
		case !slices.Contains(s.Allowed, entry):
			return nil, handler.Refuse("invalid-param", `params[%q] is %q; its entry %q is not a type the block runs on, which are %s`,
				serverTypeParam, value, entry, allowed)
		case slices.Contains(entries[:i], entry):
			return nil, handler.Refuse("invalid-param", `params[%q] is %q; its entry %q is given twice`, serverTypeParam, value, entry)
		}
	}
	return entries, nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}
