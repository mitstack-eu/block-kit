package gitopstest

import (
	"encoding/json"
	"net/http"
	"path"
)

// ServeGitHubCommit answers GitHub's GET /repos/{owner}/{repo}/commits/{sha} for the revision the request's path ends in, with the parent and paths gitops.GitHubRepository reads, and as GitHub does, 422 for a revision r never held.
func (r *Repository) ServeGitHubCommit(w http.ResponseWriter, req *http.Request) {
	revision := path.Base(req.URL.Path)
	parent, changed, err := r.Changed(req.Context(), revision)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "No commit found for SHA: " + revision})
		return
	}
	parents := []map[string]string{}
	if parent != "" {
		parents = append(parents, map[string]string{"sha": parent})
	}
	files := make([]map[string]string, 0, len(changed))
	for _, p := range changed {
		files = append(files, map[string]string{"filename": p})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": revision, "parents": parents, "files": files})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
