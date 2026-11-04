package gitops_test

// CommitManifests commits on head what differs, retrying a moved tip, and refuses a secret before any write; Revert restores the block's files to the last revision Argo CD judged healthy.

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
)

const (
	nsPath     = "apps/app-alpha/namespace.yaml"
	saPath     = "apps/app-alpha/serviceaccount.yaml"
	betaPath   = "apps/app-beta/namespace.yaml"
	dbPassword = "s3cr3t-db-password-value"
)

func alphaFiles() map[string][]byte {
	return map[string][]byte{
		nsPath: []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: app-alpha\n"),
		saPath: []byte("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: app-alpha-runtime\n  namespace: app-alpha\n"),
	}
}

func with(files map[string][]byte, path string, content []byte) map[string][]byte {
	files[path] = content
	return files
}

// wantFilesAt checks every file stands at revision with exactly its content, a nil content meaning absent.
func wantFilesAt(t *testing.T, repo gitops.Repository, revision string, files map[string][]byte) {
	t.Helper()
	for path, want := range files {
		got, ok, err := repo.ReadFile(context.Background(), revision, path)
		switch {
		case err != nil:
			t.Errorf("ReadFile(%s, %s): %v", revision, path, err)
		case want == nil && ok:
			t.Errorf("%s stands at %s with %q, want it absent", path, revision, got)
		case want != nil && (!ok || string(got) != string(want)):
			t.Errorf("%s at %s = %q (present %v), want %q", path, revision, got, ok, want)
		}
	}
}

func TestCommitManifests(t *testing.T) {
	ctx := context.Background()
	foreign := map[string][]byte{betaPath: []byte("beta\n")}
	cases := []struct {
		name        string
		seed        func(t *testing.T, repo *gitopstest.Repository) string
		files       map[string][]byte
		secrets     []string
		wantChanged bool
		wantErr     error
		check       func(t *testing.T, repo *gitopstest.Repository, seeded, rev string, err error)
	}{
		{"on an empty repository it commits the files under the message and returns the new head", nil, alphaFiles(), nil, true, nil,
			func(t *testing.T, repo *gitopstest.Repository, _, rev string, _ error) {
				head, _ := repo.Head(ctx)
				commits := repo.Commits()
				if rev == "" || rev != head || len(commits) != 1 || commits[0].Parent != "" || commits[0].Message != "app-alpha" {
					t.Errorf("revision %q, head %q, commits %+v; want one parentless commit with the message handed in at head", rev, head, commits)
				}
				wantFilesAt(t, repo, rev, alphaFiles())
			}},
		{"files already at head commit nothing and return head", func(t *testing.T, repo *gitopstest.Repository) string {
			return mustCommit(t, repo, alphaFiles())
		}, alphaFiles(), nil, false, nil,
			func(t *testing.T, repo *gitopstest.Repository, seeded, rev string, _ error) {
				if rev != seeded || len(repo.Commits()) != 1 {
					t.Errorf("revision %q with %d commits, want head %q and no new commit", rev, len(repo.Commits()), seeded)
				}
			}},
		{"one changed file commits on top of head and keeps every other path", func(t *testing.T, repo *gitopstest.Repository) string {
			if _, err := repo.Commit(ctx, "", foreign, "app-beta"); err != nil {
				t.Fatal(err)
			}
			return mustCommit(t, repo, alphaFiles())
		}, with(alphaFiles(), saPath, []byte("kind: ServiceAccount\nlabels: {tier: web}\n")), nil, true, nil,
			func(t *testing.T, repo *gitopstest.Repository, seeded, rev string, _ error) {
				commits := repo.Commits()
				if last := commits[len(commits)-1]; rev == seeded || last.Parent != seeded {
					t.Errorf("revision %q on parent %q, want a new revision on head %q", rev, last.Parent, seeded)
				}
				wantFilesAt(t, repo, rev, with(alphaFiles(), saPath, []byte("kind: ServiceAccount\nlabels: {tier: web}\n")))
				wantFilesAt(t, repo, rev, foreign)
			}},
		{"a tip another writer moves twice is re-read and the commit lands on its latest commit", func(t *testing.T, repo *gitopstest.Repository) string {
			repo.MoveTip(2, foreign)
			return ""
		}, alphaFiles(), nil, true, nil,
			func(t *testing.T, repo *gitopstest.Repository, _, rev string, _ error) {
				commits := repo.Commits()
				if len(commits) != 3 || commits[2].Revision != rev || commits[2].Parent != commits[1].Revision {
					t.Fatalf("commits %+v, want two foreign commits, then ours %q on the second", commits, rev)
				}
				wantFilesAt(t, repo, rev, alphaFiles())
				wantFilesAt(t, repo, rev, foreign)
			}},
		{"a tip that keeps moving ends in ErrStaleBase after a bounded number of tries", func(t *testing.T, repo *gitopstest.Repository) string {
			repo.MoveTip(1000, foreign)
			return ""
		}, alphaFiles(), nil, false, gitops.ErrStaleBase,
			func(t *testing.T, repo *gitopstest.Repository, _, _ string, _ error) {
				// Each try lands one foreign commit and none of ours.
				if tries := len(repo.Commits()); tries < 2 || tries >= 1000 {
					t.Errorf("CommitManifests tried %d times, want a retry and a bound well below 1000", tries)
				}
				head, _ := repo.Head(ctx)
				wantFilesAt(t, repo, head, map[string][]byte{nsPath: nil})
			}},
		{"a file holding a secret verbatim is refused before any write", nil,
			with(alphaFiles(), "apps/app-alpha/configmap.yaml", []byte("kind: ConfigMap\ndata:\n  DATABASE_URL: postgres://app:"+dbPassword+"@db/app\n")),
			[]string{"unrelated-value", dbPassword}, false, gitops.ErrSecretInManifest, refusedNaming("apps/app-alpha/configmap.yaml")},
		{"a file holding a secret as its standard base64 is refused before any write", nil,
			with(alphaFiles(), "apps/app-alpha/secret.yaml", []byte("kind: Secret\ndata:\n  password: "+base64.StdEncoding.EncodeToString([]byte(dbPassword))+"\n")),
			[]string{dbPassword}, false, gitops.ErrSecretInManifest, refusedNaming("apps/app-alpha/secret.yaml")},
		{"an empty secret value refuses nothing", nil, alphaFiles(), []string{"", "never-in-a-file"}, true, nil,
			func(t *testing.T, repo *gitopstest.Repository, _, _ string, _ error) {
				if got := len(repo.Commits()); got != 1 {
					t.Errorf("Commits = %d, want 1", got)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitopstest.NewRepository()
			seeded := ""
			if tc.seed != nil {
				seeded = tc.seed(t, repo)
			}

			rev, changed, err := gitops.CommitManifests(ctx, repo, tc.files, "app-alpha", tc.secrets)

			if changed != tc.wantChanged || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("CommitManifests = %q, %v, %v; want changed %v and error %v", rev, changed, err, tc.wantChanged, tc.wantErr)
			}
			if err != nil {
				for _, secret := range tc.secrets {
					if secret != "" && (strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(secret)))) {
						t.Errorf("error %q carries a secret value, verbatim or base64", err)
					}
				}
			}
			tc.check(t, repo, seeded, rev, err)
		})
	}
}

func mustCommit(t *testing.T, repo *gitopstest.Repository, files map[string][]byte) string {
	t.Helper()
	rev, _, err := gitops.CommitManifests(context.Background(), repo, files, "seed", nil)
	if err != nil {
		t.Fatalf("seeding CommitManifests: %v", err)
	}
	return rev
}

// refusedNaming checks a refused manifest set names its offending path and writes nothing, not even its clean files.
func refusedNaming(path string) func(t *testing.T, repo *gitopstest.Repository, _, _ string, err error) {
	return func(t *testing.T, repo *gitopstest.Repository, _, _ string, err error) {
		t.Helper()
		if got := len(repo.Commits()); got != 0 {
			t.Errorf("Commits = %d, want 0", got)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("refusal %q does not name the offending path %s", err, path)
		}
	}
}

const (
	revertApp       = "app-alpha"
	alphaDeployment = "apps/app-alpha/deployment.yaml"
	alphaService    = "apps/app-alpha/service.yaml"
	alphaConfig     = "apps/app-alpha/config.yaml"
	alphaWorker     = "apps/app-alpha/worker.yaml"
)

// errSaying stands for an error whose text holds the fragment, in a case with no sentinel to match.
type errSaying string

func (e errSaying) Error() string { return string(e) }

func errMatches(err, want error) bool {
	var fragment errSaying
	switch {
	case want == nil:
		return err == nil
	case errors.As(want, &fragment):
		return err != nil && strings.Contains(err.Error(), string(fragment))
	default:
		return errors.Is(err, want)
	}
}

// revertHistory lands commits on the tip and records Argo CD's verdict on a revision, as the cluster posts it.
type revertHistory struct {
	t       *testing.T
	repo    *gitopstest.Repository
	notes   *gitopstest.Notifications
	healthy string
}

func (h *revertHistory) commit(changes map[string][]byte) string {
	h.t.Helper()
	head, _ := h.repo.Head(context.Background())
	rev, err := h.repo.Commit(context.Background(), head, changes, "seed")
	if err != nil {
		h.t.Fatalf("seeding a commit: %v", err)
	}
	return rev
}

func (h *revertHistory) judgedHealthy(rev string) string {
	h.notes.Set(gitops.Notification{App: revertApp, Revision: rev, Phase: "Succeeded", Health: "Healthy"})
	h.healthy = rev
	return rev
}

func (h *revertHistory) judgedDegradedSince(rev string) string {
	h.notes.Set(gitops.Notification{App: revertApp, Revision: rev, Phase: "Succeeded", Health: "Degraded"})
	return rev
}

func (h *revertHistory) judgedFailed(rev string) string {
	h.notes.Set(gitops.Notification{App: revertApp, Revision: rev, Phase: "Failed", Health: "Degraded"})
	return rev
}

func ownsAlpha(gitops.Repository) gitops.Owned {
	return func(context.Context, string) ([]string, error) {
		return []string{alphaDeployment, alphaService, alphaConfig, alphaWorker}, nil
	}
}

// ownsWorkerWhileHeld owns the worker's file only at a revision holding it, as a block reading its process list from the repository does.
func ownsWorkerWhileHeld(repo gitops.Repository) gitops.Owned {
	return func(ctx context.Context, revision string) ([]string, error) {
		if _, held, err := repo.ReadFile(ctx, revision, alphaWorker); err != nil || held {
			return []string{alphaDeployment, alphaWorker}, err
		}
		return []string{alphaDeployment}, nil
	}
}

func image(tag string) []byte { return []byte("image: " + tag + "\n") }

func TestRevert(t *testing.T) {
	ctx := context.Background()
	beta := []byte("beta: b1\n")
	cases := []struct {
		name string
		// history lands commits and verdicts, and returns the revision Argo CD failed.
		history     func(h *revertHistory) string
		owned       func(gitops.Repository) gitops.Owned
		wantErr     error
		wantFiles   map[string][]byte
		wantCommits int
	}{
		{"a head two commits past the last healthy revision gets every owned file back as it stood there in one commit", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1"), alphaService: []byte("port: 80\n")}))
			h.commit(map[string][]byte{alphaDeployment: image("v2")})
			return h.judgedFailed(h.commit(map[string][]byte{alphaService: []byte("port: 81\n"), alphaConfig: []byte("debug: true\n")}))
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: image("v1"), alphaService: []byte("port: 80\n"), alphaConfig: nil}, 4},
		{"a file the block owned at the healthy revision and dropped since comes back", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1"), alphaWorker: []byte("worker: v1\n")}))
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2"), alphaWorker: nil}))
		}, ownsWorkerWhileHeld, nil, map[string][]byte{alphaDeployment: image("v1"), alphaWorker: []byte("worker: v1\n")}, 3},
		{"another block's file committed after the healthy revision stays as its commit left it", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			h.commit(map[string][]byte{betaPath: beta})
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: image("v1"), betaPath: beta}, 4},
		{"a later commit changing only another block's file lets the revert land on top of it", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
			h.commit(map[string][]byte{betaPath: beta})
			return failed
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: image("v1"), betaPath: beta}, 4},
		{"a first deploy Argo CD never judged healthy has every file its commit added removed", func(h *revertHistory) string {
			h.commit(map[string][]byte{betaPath: beta})
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v1"), alphaService: []byte("port: 80\n")}))
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: nil, alphaService: nil, betaPath: beta}, 3},
		{"a first deploy into an empty repository Argo CD never judged healthy has every file removed", func(h *revertHistory) string {
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v1"), alphaService: []byte("port: 80\n")}))
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: nil, alphaService: nil}, 2},
		{"a first commit whose sync succeeded and whose newest notification reads degraded keeps its files, since it may have run healthy before", func(h *revertHistory) string {
			h.judgedDegradedSince(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
		}, ownsAlpha, errSaying("may have run healthy"), map[string][]byte{alphaDeployment: image("v2")}, 2},
		{"a first commit whose newest notification is a failed sync keeps its files, since a later sync may have failed after a healthy one", func(h *revertHistory) string {
			h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
		}, ownsAlpha, errSaying("may have run healthy"), map[string][]byte{alphaDeployment: image("v2")}, 2},
		{"a first commit Argo CD's record no longer holds, with no healthy revision since, reverts nothing", func(h *revertHistory) string {
			h.commit(map[string][]byte{alphaDeployment: image("v1")})
			return h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
		}, ownsAlpha, errSaying("may have run healthy"), map[string][]byte{alphaDeployment: image("v2")}, 2},
		{"a head moved past the failed revision by a commit changing the block's files gets no revert", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
			h.commit(map[string][]byte{alphaDeployment: image("v3")})
			return failed
		}, ownsAlpha, gitops.ErrHeadMoved, map[string][]byte{alphaDeployment: image("v3")}, 3},
		{"a head moved past the failed revision by a commit deleting a file the block owned there gets no revert", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1"), alphaWorker: []byte("worker: v1\n")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2"), alphaWorker: []byte("worker: v2\n")}))
			h.commit(map[string][]byte{alphaWorker: nil})
			return failed
		}, ownsWorkerWhileHeld, gitops.ErrHeadMoved, map[string][]byte{alphaDeployment: image("v2"), alphaWorker: nil}, 3},
		{"a failed head whose owned files stand as at the healthy revision commits nothing and answers head", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			return h.judgedFailed(h.commit(map[string][]byte{betaPath: beta}))
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: image("v1"), betaPath: beta}, 2},
		{"a failed revision the repository never held fails and commits nothing", func(h *revertHistory) string {
			h.commit(map[string][]byte{alphaDeployment: image("v1")})
			return "no-such-revision"
		}, ownsAlpha, errSaying("no-such-revision is not an ancestor of head"), nil, 1},
		{"a thousand failed deploys with none judged healthy fail the revert, which reads only a bounded stretch of history", func(h *revertHistory) string {
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v0")}))
			for i := 1; i < 1000; i++ {
				failed = h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image(strconv.Itoa(i))}))
			}
			return failed
		}, ownsAlpha, errSaying("judged none of the 50 commits before"), map[string][]byte{alphaDeployment: image("999")}, 1000},
		{"a notification the platform cannot answer fails the revert and commits nothing", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
			h.notes.FailWith(errors.New("platform unreachable"))
			return failed
		}, ownsAlpha, errSaying("platform unreachable"), map[string][]byte{alphaDeployment: image("v2")}, 2},
		{"a commit of the block's files landing while the revert runs leaves the revert unlanded", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
			h.repo.MoveTip(1, map[string][]byte{alphaDeployment: image("v3")})
			return failed
		}, ownsAlpha, gitops.ErrHeadMoved, map[string][]byte{alphaDeployment: image("v3")}, 3},
		{"another block's commit landing while the revert runs is read again and the revert lands on it", func(h *revertHistory) string {
			h.judgedHealthy(h.commit(map[string][]byte{alphaDeployment: image("v1")}))
			failed := h.judgedFailed(h.commit(map[string][]byte{alphaDeployment: image("v2")}))
			h.repo.MoveTip(1, map[string][]byte{betaPath: beta})
			return failed
		}, ownsAlpha, nil, map[string][]byte{alphaDeployment: image("v1"), betaPath: beta}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &revertHistory{t: t, repo: gitopstest.NewRepository(), notes: gitopstest.NewNotifications()}
			failed := tc.history(h)
			seeded := len(h.repo.Commits())

			got, err := gitops.Revert(ctx, h.repo, h.notes, gitops.BlockRevert{App: revertApp, Failed: failed, Owned: tc.owned(h.repo), Message: "revert"})

			commits := h.repo.Commits()
			head, _ := h.repo.Head(ctx)
			if !errMatches(err, tc.wantErr) || len(commits) != tc.wantCommits {
				t.Fatalf("Revert(%s) = %+v, %v with %d commits; want error %v and %d commits", failed, got, err, len(commits), tc.wantErr, tc.wantCommits)
			}
			wantFilesAt(t, h.repo, head, tc.wantFiles)
			if err != nil {
				return
			}
			if got.Revision != head || got.Healthy != h.healthy {
				t.Errorf("Revert = %+v; want the head %q and the healthy revision %q", got, head, h.healthy)
			}
			if last := commits[len(commits)-1]; len(commits) > seeded && last.Message != "revert" {
				t.Errorf("the commit at head is %+v; want the revert, carrying the message handed in", last)
			}
		})
	}
}

func TestSecretValues_CollectsEveryValueInSecretInputsAppSecretsAndSecrets(t *testing.T) {
	d := contract.Desired{
		Params:       map[string]string{"region": "fsn1"},
		SecretInputs: map[string]string{"dbPassword": "secret-input-1"},
		AppSecrets:   map[string]string{"STRIPE_KEY": "app-secret-1"},
		Secrets: map[string]map[string]string{
			"github":  {"token": "ghs_platformMintedToken"},
			"hetzner": {"apiToken": "hz-token", "sshKey": "ssh-key-material"},
		},
	}

	got := gitops.SecretValues(d)

	sort.Strings(got)
	if want := []string{"app-secret-1", "ghs_platformMintedToken", "hz-token", "secret-input-1", "ssh-key-material"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SecretValues = %q, want %q and never a param value", got, want)
	}
}

const (
	sliceProjectPath = "cluster/app-s4168-c1-app-project.json"
	sliceGithubToken = "ghs_a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8"
	sliceClusterCred = "k3s-credential-Zm9vYmFyYmF6cXV4"
	slicePAT         = "github_pat_fake0123456789"
)

func sliceRequest() contract.Desired {
	return contract.Desired{
		Params: map[string]string{"appName": "s4168-c1", "repoUrl": "https://github.com/mitstack-eu/k4168c", "quotaCpu": "1", "quotaMemory": "1Gi"},
		Secrets: map[string]map[string]string{
			"github":  {"token": sliceGithubToken, "owner": "mitstack-eu"},
			"cluster": {"clusterCredential": sliceClusterCred},
		},
	}
}

func TestCommitManifests_TheSliceRequestsSecrets(t *testing.T) {
	project, err := os.ReadFile("testdata/slice-app-project/app-s4168-c1-app-project.json")
	if err != nil {
		t.Fatalf("reading the rendered AppProject: %v", err)
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name  string
		files map[string][]byte
		want  error
	}{
		{"the rendered AppProject of a github org repo commits, since the owner is no credential", map[string][]byte{sliceProjectPath: project}, nil},
		{"the github token verbatim is refused", map[string][]byte{"cluster/leak.json": []byte(`{"value":"` + sliceGithubToken + `"}`)}, gitops.ErrSecretInManifest},
		{"the github token as standard base64 is refused", map[string][]byte{"cluster/leak.json": []byte(`{"value":"` + b64(sliceGithubToken) + `"}`)}, gitops.ErrSecretInManifest},
		{"the cluster credential verbatim is refused", map[string][]byte{"cluster/leak.json": []byte(`{"value":"` + sliceClusterCred + `"}`)}, gitops.ErrSecretInManifest},
		{"the cluster credential as standard base64 is refused", map[string][]byte{"cluster/leak.json": []byte(`{"value":"` + b64(sliceClusterCred) + `"}`)}, gitops.ErrSecretInManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := gitops.CommitManifests(context.Background(), gitopstest.NewRepository(), tc.files, "app-s4168-c1", gitops.SecretValues(sliceRequest()))

			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("CommitManifests = %v, want %v", err, tc.want)
			}
			for _, v := range gitops.SecretValues(sliceRequest()) {
				if _, _, single := gitops.CommitManifests(context.Background(), gitopstest.NewRepository(), tc.files, "probe", []string{v}); tc.want == nil && err != nil && single != nil {
					t.Errorf("the secret value %q alone refuses the AppProject: %v", v, single)
				}
			}
		})
	}
}

func TestSecretValues_CollectsTheCredentialsAndNeverTheGithubOwner(t *testing.T) {
	d := sliceRequest()
	d.Secrets["github"]["pat"] = slicePAT

	got := gitops.SecretValues(d)

	for _, want := range []string{sliceGithubToken, sliceClusterCred, slicePAT} {
		if !slices.Contains(got, want) {
			t.Errorf("SecretValues = %q, want it to contain the credential %q", got, want)
		}
	}
	if slices.Contains(got, "mitstack-eu") {
		t.Errorf("SecretValues = %q, want the github owner omitted", got)
	}
}
