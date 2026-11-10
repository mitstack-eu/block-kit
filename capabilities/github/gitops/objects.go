package gitops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Object is one Kubernetes object a manifest declares, with an empty Namespace when the manifest names none, as a cluster-scoped object's does.
type Object struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// String names o as kubectl does, its kind and API group, then its namespace and name.
func (o Object) String() string {
	kind := o.Kind
	if o.Group != "" {
		kind += "." + o.Group
	}
	if o.Namespace == "" {
		return kind + " " + o.Name
	}
	return kind + " " + o.Namespace + "/" + o.Name
}

// manifestHead is the part of a manifest that names its object.
type manifestHead struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

// object reads the object h names, declaring none when h names neither apiVersion nor kind, as a document that is no Kubernetes manifest does.
func (h manifestHead) object() (Object, bool, error) {
	if h.APIVersion == "" && h.Kind == "" {
		return Object{}, false, nil
	}
	if h.APIVersion == "" || h.Kind == "" || h.Metadata.Name == "" {
		return Object{}, false, fmt.Errorf("the manifest names no apiVersion, kind and metadata.name: %q, %q, %q", h.APIVersion, h.Kind, h.Metadata.Name)
	}
	group, _, versioned := strings.Cut(h.APIVersion, "/")
	if !versioned {
		group = ""
	}
	return Object{Group: group, Kind: h.Kind, Namespace: h.Metadata.Namespace, Name: h.Metadata.Name}, true, nil
}

// Objects reads the Kubernetes objects content declares, as one JSON object, a JSON array of them, or YAML documents whose metadata is a block mapping, and a document naming neither apiVersion nor kind declares none.
func Objects(content []byte) ([]Object, error) {
	text := bytes.TrimSpace(content)
	if len(text) > 0 && (text[0] == '{' || text[0] == '[') {
		return jsonObjects(text)
	}
	var objects []Object
	for i, document := range yamlDocuments(string(content)) {
		head, err := readYAMLHead(document)
		if err != nil {
			return nil, fmt.Errorf("YAML document %d: %w", i+1, err)
		}
		o, declares, err := head.object()
		if err != nil {
			return nil, fmt.Errorf("YAML document %d: %w", i+1, err)
		}
		if declares {
			objects = append(objects, o)
		}
	}
	return objects, nil
}

func jsonObjects(text []byte) ([]Object, error) {
	var heads []manifestHead
	var err error
	if text[0] == '{' {
		heads = make([]manifestHead, 1)
		err = json.Unmarshal(text, &heads[0])
	} else {
		err = json.Unmarshal(text, &heads)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the JSON manifest: %w", err)
	}
	var objects []Object
	for _, h := range heads {
		o, declares, err := h.object()
		if err != nil {
			return nil, err
		}
		if declares {
			objects = append(objects, o)
		}
	}
	return objects, nil
}

// yamlDocuments splits text at each line that starts a document with ---.
func yamlDocuments(text string) [][]string {
	documents := [][]string{nil}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "---" || strings.HasPrefix(line, "--- ") {
			documents = append(documents, nil)
			continue
		}
		documents[len(documents)-1] = append(documents[len(documents)-1], line)
	}
	return documents
}

// readYAMLHead reads apiVersion, kind and metadata's name and namespace from a document's top-level keys.
func readYAMLHead(lines []string) (manifestHead, error) {
	var h manifestHead
	inMetadata, child := false, -1
	for _, line := range lines {
		text := withoutYAMLComment(line)
		if strings.TrimSpace(text) == "" || text == "..." {
			continue
		}
		indent := len(text) - len(strings.TrimLeft(text, " "))
		key, raw, isKey := strings.Cut(strings.TrimSpace(text), ":")
		var field *string
		switch {
		case indent == 0:
			inMetadata = isKey && key == "metadata"
			if inMetadata && strings.TrimSpace(raw) != "" {
				return manifestHead{}, errors.New("metadata is not a block mapping, the only form read")
			}
			field = map[string]*string{"apiVersion": &h.APIVersion, "kind": &h.Kind}[key]
		case inMetadata && (child < 0 || indent == child):
			child = indent
			field = map[string]*string{"name": &h.Metadata.Name, "namespace": &h.Metadata.Namespace}[key]
		}
		if field == nil || !isKey {
			continue
		}
		value, err := yamlScalar(raw)
		if err != nil {
			return manifestHead{}, fmt.Errorf("%s: %w", key, err)
		}
		*field = value
	}
	return h, nil
}

// withoutYAMLComment drops a comment, which starts at a # opening the line or following a space outside quotes.
func withoutYAMLComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

// yamlScalar reads a plain, single-quoted or double-quoted scalar, the forms a manifest names its object in.
func yamlScalar(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return "", nil
	case value[0] == '"':
		return strconv.Unquote(value)
	case value[0] == '\'' && len(value) > 1 && value[len(value)-1] == '\'':
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	case strings.ContainsRune("'|>&*!{[", rune(value[0])):
		return "", fmt.Errorf("%q is not a plain or quoted scalar", value)
	}
	return value, nil
}
