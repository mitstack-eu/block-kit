package contract

import (
	"maps"
	"slices"
)

// SecretValues is every secret value a request carries, kept apart by where the request carries it, so each reader names the parts it reads.
type SecretValues struct {
	Connections  []string
	SecretInputs []string
	AppSecrets   []string
	OwnSecrets   []string
	Credentials  []string
}

func (v SecretValues) All() []string {
	return slices.Concat(v.Connections, v.SecretInputs, v.AppSecrets, v.OwnSecrets, v.Credentials)
}

// SecretValues lists the secret values d carries, each part sorted and without an empty value, which is an unset optional secret.
func (d Desired) SecretValues() SecretValues {
	var connections []string
	for _, group := range d.Secrets {
		connections = append(connections, slices.Collect(maps.Values(group))...)
	}
	return SecretValues{
		Connections:  presentSorted(connections),
		SecretInputs: valuesOf(d.SecretInputs),
		AppSecrets:   valuesOf(d.AppSecrets),
		OwnSecrets:   valuesOf(d.OwnSecrets),
	}
}

func (r Request) SecretValues() SecretValues {
	values := r.Desired().SecretValues()
	credentials := append(slices.Collect(maps.Values(r.EnteredCredential)), r.OBO)
	if r.EventsCallback != nil {
		credentials = append(credentials, r.EventsCallback.Token)
	}
	values.Credentials = presentSorted(credentials)
	return values
}

func valuesOf(m map[string]string) []string {
	return presentSorted(slices.Collect(maps.Values(m)))
}

// presentSorted drops each empty value and sorts the rest, so a part reads the same on every call.
func presentSorted(values []string) []string {
	values = slices.DeleteFunc(values, func(v string) bool { return v == "" })
	slices.Sort(values)
	return values
}
