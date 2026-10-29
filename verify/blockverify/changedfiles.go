package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// namedFileLimit caps the files a missing-bump message names, so a large pull request still reads as one line.
const namedFileLimit = 3

// imageFilesChanged reads a NUL-separated list, as git's -z --name-only output writes it, and keeps each file the image is built from.
func imageFilesChanged(changedFiles string) ([]string, error) {
	raw, err := os.ReadFile(changedFiles)
	if err != nil {
		return nil, fmt.Errorf("changed-files list %s could not be read, so the files the pull request changes are unknown: %v", changedFiles, err)
	}
	var files []string
	for _, path := range strings.Split(string(raw), "\x00") {
		if path != "" && builtIntoImage(path) {
			files = append(files, path)
		}
	}
	return files, nil
}

// builtIntoImage leaves out Markdown, anything under .github/ and the root LICENSE, which no block image is built from.
func builtIntoImage(path string) bool {
	switch ext := strings.ToLower(filepath.Ext(path)); {
	case ext == ".md" || ext == ".markdown":
		return false
	case strings.HasPrefix(path, ".github/"):
		return false
	case path == "LICENSE":
		return false
	}
	return true
}

func namedFiles(files []string) string {
	if len(files) > namedFileLimit {
		return fmt.Sprintf("%s and %d more", strings.Join(files[:namedFileLimit], ", "), len(files)-namedFileLimit)
	}
	last := len(files) - 1
	if last == 0 {
		return files[0]
	}
	return strings.Join(files[:last], ", ") + " and " + files[last]
}
