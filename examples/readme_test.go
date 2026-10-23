// Package examples_test keeps each README code fence that names an example file equal to that file.
package examples_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fenceSource matches a lead-in line ending in a link to a file under examples/ and a colon, which marks the fence after it as that file.
var fenceSource = regexp.MustCompile(`\]\((examples/[^)\s]+)\):$`)

type takenFence struct {
	line int
	path string
	body string
}

func takenFences(readme string) []takenFence {
	var out []takenFence
	lines := strings.Split(readme, "\n")
	source := ""
	for i := 0; i < len(lines); i++ {
		text := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(text, "```") {
			if text != "" {
				source = ""
				if m := fenceSource.FindStringSubmatch(text); m != nil {
					source = m[1]
				}
			}
			continue
		}
		open := i + 1
		var body []string
		for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
			body = append(body, lines[i])
		}
		if source != "" {
			out = append(out, takenFence{line: open, path: source, body: strings.Join(body, "\n") + "\n"})
		}
		source = ""
	}
	return out
}

func TestREADME_EachFenceTakenFromAnExampleEqualsTheFileItNames(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}

	fences := takenFences(string(readme))

	if len(fences) == 0 {
		t.Fatal("README.md takes no code fence from a file under examples/, so nothing holds its minimal block to the example")
	}
	for _, f := range fences {
		want, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(f.path)))
		if err != nil {
			t.Errorf("README.md line %d takes its fence from %s, which cannot be read: %v", f.line, f.path, err)
			continue
		}
		if f.body != string(want) {
			t.Errorf("README.md line %d: the fence taken from %s differs from that file, so copy the file into the fence", f.line, f.path)
		}
	}
}
