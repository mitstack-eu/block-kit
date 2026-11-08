package observe

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

// AWSSubscriptionTag is the tag key the platform's operator trust boundary requires on every object a subscription creates.
const AWSSubscriptionTag = "mitstack:subscription"

const HetznerSubscriptionLabel = "mitstack.dev/subscription"

// ContainerSubscriptionLabel is the label a container carries with its subscription's id.
const ContainerSubscriptionLabel = AWSSubscriptionTag

// AWSRoleTag with the value AWSRoleEgressProxy marks an EC2 instance as the egress proxy rather than the host.
const (
	AWSRoleTag         = "mitstack:role"
	AWSRoleEgressProxy = "egress-proxy"
)

// Tagged has an empty Type for an object the lister cannot type, and its ID then names the object in full.
type Tagged struct {
	Type string
	ID   string
}

// TaggedLister lists everything carrying a subscription's id.
type TaggedLister interface {
	ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error)
}

type AWSTaggingAPI interface {
	ResourceARNs(ctx context.Context, d contract.Desired, tagKey, tagValue string) ([]string, error)
}

// AWSRoleTaggingAPI lists the ARNs carrying the subscription's tag and the given AWSRoleTag value.
type AWSRoleTaggingAPI interface {
	RoleARNs(ctx context.Context, d contract.Desired, subscriptionID, role string) ([]string, error)
}

// SSMAssociationsAPI lists the ids of the associations that run the named SSM document.
type SSMAssociationsAPI interface {
	AssociationIDs(ctx context.Context, d contract.Desired, document string) ([]string, error)
}

// AWSTagged lists the AWS resources carrying a subscription's id under AWSSubscriptionTag.
type AWSTagged struct {
	API AWSTaggingAPI
}

// awsResourceTypes maps an ARN's service and resource type to the catalogue type id the platform serves for it.
var awsResourceTypes = map[string]string{
	"ec2/instance":            "aws.ec2-instance",
	"ec2/vpc":                 "aws.vpc",
	"ec2/security-group":      "aws.security-group",
	"ec2/volume":              "aws.ebs-volume",
	"iam/role":                "aws.iam-role",
	"ecr/repository":          "aws.ecr-repository",
	"iam/instance-profile":    "aws.iam-instance-profile",
	"ec2/elastic-ip":          "aws.elastic-ip",
	"ec2/security-group-rule": "aws.security-group-rule",
	"dlm/policy":              "aws.dlm-lifecycle-policy",
	"ssm/document":            "aws.ssm-document",
	"ssm/parameter":           "aws.ssm-parameter",
}

// awsPathedTypes are the IAM types whose ARN carries a path before the name.
var awsPathedTypes = map[string]bool{"aws.iam-role": true, "aws.iam-instance-profile": true}

func (l AWSTagged) ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error) {
	arns, err := l.API.ResourceARNs(ctx, d, AWSSubscriptionTag, subscriptionID)
	if err != nil {
		return nil, err
	}
	var proxies []string
	if r, ok := l.API.(AWSRoleTaggingAPI); ok {
		if proxies, err = r.RoleARNs(ctx, d, subscriptionID, AWSRoleEgressProxy); err != nil {
			return nil, err
		}
	}
	out := make([]Tagged, 0, len(arns))
	for _, arn := range arns {
		t := taggedARN(arn)
		if t.Type == "aws.ec2-instance" && slices.Contains(proxies, arn) {
			t.Type = "aws.egress-proxy"
		}
		out = append(out, t)
	}
	a, ok := l.API.(SSMAssociationsAPI)
	if !ok {
		return out, nil
	}
	for _, doc := range slices.Clone(out) {
		if doc.Type != "aws.ssm-document" {
			continue
		}
		ids, err := a.AssociationIDs(ctx, d, doc.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			out = append(out, Tagged{Type: "aws.ssm-association", ID: id})
		}
	}
	return out, nil
}

// taggedARN types one ARN, arn:<partition>:<service>:<region>:<account>:<resource>.
func taggedARN(arn string) Tagged {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return Tagged{ID: arn}
	}
	service, resource := parts[2], parts[5]
	if service == "s3" && !strings.Contains(resource, "/") {
		return Tagged{Type: "aws.s3-bucket", ID: resource}
	}
	kind, id, ok := strings.Cut(resource, "/")
	typ := awsResourceTypes[service+"/"+kind]
	if !ok || typ == "" {
		return Tagged{ID: arn}
	}
	if awsPathedTypes[typ] {
		id = path.Base(id)
	}
	// SSM requires a name in a hierarchy to start with a slash, and the name's ARN drops it.
	if typ == "aws.ssm-parameter" && strings.Contains(id, "/") {
		id = "/" + id
	}
	return Tagged{Type: typ, ID: id}
}

// HetznerObject carries its Kind as the Hetzner Cloud API names it.
type HetznerObject struct {
	Kind string
	ID   string
}

type HetznerLabelAPI interface {
	ListByLabel(ctx context.Context, d contract.Desired, selector string) ([]HetznerObject, error)
}

type HetznerLabelled struct {
	API HetznerLabelAPI
}

var hetznerTypes = map[string]string{
	"server":     "hetzner.server",
	"primary_ip": "hetzner.primary-ip",
	"firewall":   "hetzner.firewall",
	"volume":     "hetzner.volume",
	"ssh_key":    "hetzner.ssh-key",
	"network":    "hetzner.network",
}

func (l HetznerLabelled) ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error) {
	objects, err := l.API.ListByLabel(ctx, d, HetznerSubscriptionLabel+"="+subscriptionID)
	if err != nil {
		return nil, err
	}
	out := make([]Tagged, 0, len(objects))
	for _, o := range objects {
		if typ, ok := hetznerTypes[o.Kind]; ok {
			out = append(out, Tagged{Type: typ, ID: o.ID})
		} else {
			out = append(out, Tagged{ID: o.Kind + "/" + o.ID})
		}
	}
	return out, nil
}

const GitHubSubscriptionTrailerKey = "Mitstack-Subscription"

// GitHubSubscriptionTopic keeps the id's case, since a topic is matched exactly and the platform already issues every id in lowercase.
func GitHubSubscriptionTopic(subscriptionID string) string {
	return "mitstack-" + strings.ReplaceAll(subscriptionID, "_", "-")
}

// GitHubSubscriptionTrailer is the trailer line that closes the message of every commit a subscription writes.
func GitHubSubscriptionTrailer(subscriptionID string) string {
	return GitHubSubscriptionTrailerKey + ": " + subscriptionID
}

type GitHubRepositoryTopics struct {
	FullName string
	Topics   []string
}

type GitHubWebhook struct {
	ID  string
	URL string
}

// GitHubCommitPaths is one commit on a repository's default branch, with the paths it wrote that the branch still holds.
type GitHubCommitPaths struct {
	// SHA is the commit's id; a trailer commit with none makes the lookup fail instead of reading absent.
	SHA     string
	Message string
	Paths   []string
}

// GitHubTagAPI may answer RepositoriesWithTopic with repositories lacking the topic, since the lister keeps only those carrying it.
type GitHubTagAPI interface {
	RepositoriesWithTopic(ctx context.Context, d contract.Desired, topic string) ([]GitHubRepositoryTopics, error)
	Webhooks(ctx context.Context, d contract.Desired, repo string) ([]GitHubWebhook, error)
	ActionsSecretNames(ctx context.Context, d contract.Desired, repo string) ([]string, error)
	ActionsVariableNames(ctx context.Context, d contract.Desired, repo string) ([]string, error)
	Commits(ctx context.Context, d contract.Desired, repo string) ([]GitHubCommitPaths, error)
}

// GitHubMainFileAPI reads a file on a repository's default branch, which GitHubTagged needs when the subscription records a scaffold contribution that created files.
type GitHubMainFileAPI interface {
	// MainFile answers false when the default branch holds no file at path.
	MainFile(ctx context.Context, d contract.Desired, repo, path string) ([]byte, bool, error)
}

// GitHubTagged lists what a subscription holds on the repositories carrying its topic and on those its github.repository or github.gitops-repository requirements bind, where a webhook counts only when it points at ProcessorURL, and each scaffold contribution it records while main holds a file the contribution created.
type GitHubTagged struct {
	API GitHubTagAPI
	// RepositoryType defaults to github.repository.
	RepositoryType string
	ProcessorURL   string
	// Secrets and Variables are the fixed names the block writes, so another block's secret on the same repository is not counted.
	Secrets   []string
	Variables []string
	// CommitType, when set, counts a repository's trailer commits once as that type while the branch holds a path they wrote; otherwise each path they wrote counts as a github.workflow-file.
	CommitType string
}

func (l GitHubTagged) topicRepositories(ctx context.Context, d contract.Desired, subscriptionID string) ([]string, error) {
	if subscriptionID == "" {
		return nil, errors.New("listing GitHub resources needs a subscription id, since an empty one names no topic or trailer of its own")
	}
	topic := GitHubSubscriptionTopic(subscriptionID)
	repos, err := l.API.RepositoriesWithTopic(ctx, d, topic)
	if err != nil {
		return nil, err
	}
	var tagged []string
	for _, r := range repos {
		if slices.Contains(r.Topics, topic) {
			tagged = append(tagged, r.FullName)
		}
	}
	slices.Sort(tagged)
	return slices.Compact(tagged), nil
}

// newestTrailerCommit names the first repository, topic-tagged before bound, whose commits carry the subscription's trailer, and its newest such commit, taking Commits to list newest first.
func (l GitHubTagged) newestTrailerCommit(ctx context.Context, d contract.Desired, subscriptionID string) (string, string, error) {
	tagged, err := l.topicRepositories(ctx, d, subscriptionID)
	if err != nil {
		return "", "", err
	}
	for _, repo := range append(tagged, boundRepositories(d)...) {
		commits, err := l.API.Commits(ctx, d, repo)
		if err != nil {
			return "", "", err
		}
		for _, c := range commits {
			switch {
			case !carriesTrailer(c.Message, GitHubSubscriptionTrailer(subscriptionID)):
			case c.SHA == "":
				return "", "", fmt.Errorf("observe: a commit of %s carries the subscription's trailer but the API gave no sha", repo)
			default:
				return repo, c.SHA, nil
			}
		}
	}
	return "", "", nil
}

func (l GitHubTagged) ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error) {
	tagged, err := l.topicRepositories(ctx, d, subscriptionID)
	if err != nil {
		return nil, err
	}
	var out []Tagged
	for _, repo := range tagged {
		out = append(out, Tagged{Type: cmp.Or(l.RepositoryType, "github.repository"), ID: repo})
		on, err := l.onRepository(ctx, d, repo, subscriptionID, true)
		if err != nil {
			return nil, err
		}
		out = append(out, on...)
	}
	for _, repo := range boundRepositories(d) {
		if slices.Contains(tagged, repo) {
			continue
		}
		on, err := l.onRepository(ctx, d, repo, subscriptionID, false)
		if err != nil {
			return nil, err
		}
		out = append(out, on...)
	}
	contributions, err := l.contributions(ctx, d)
	if err != nil {
		return nil, err
	}
	return append(out, contributions...), nil
}

const githubContributionType = "github.scaffold-contribution"

// landedFile is one file of the landing record capabilities/github/landing keeps in a resource's identity, decoded here since that package imports this one.
type landedFile struct {
	Path    string `json:"path"`
	Blob    string `json:"blob"`
	Created bool   `json:"created"`
}

// contributions lists each recorded scaffold contribution by the repository its landing record names, since another block commits its files under no trailer of this subscription's.
func (l GitHubTagged) contributions(ctx context.Context, d contract.Desired) ([]Tagged, error) {
	var out []Tagged
	for _, name := range slices.Sorted(maps.Keys(d.RecordedResources)) {
		r := d.RecordedResources[name]
		if r.Type != githubContributionType {
			continue
		}
		repo, created := createdFiles(r.Identity)
		if len(created) == 0 {
			continue
		}
		held, err := l.mainHoldsOne(ctx, d, repo, created)
		if err != nil {
			return nil, err
		}
		if held {
			out = append(out, Tagged{Type: githubContributionType, ID: repo})
		}
	}
	return out, nil
}

// createdFiles returns the repository a landing record names and the files it created there, and no file for an identity holding no record.
func createdFiles(identity map[string]string) (string, []landedFile) {
	var files []landedFile
	if identity["repository"] == "" || json.Unmarshal([]byte(identity["files"]), &files) != nil {
		return "", nil
	}
	return identity["repository"], slices.DeleteFunc(files, func(f landedFile) bool { return !f.Created })
}

// mainHoldsOne reports whether main of repo holds one of files at its recorded blob.
func (l GitHubTagged) mainHoldsOne(ctx context.Context, d contract.Desired, repo string, files []landedFile) (bool, error) {
	api, ok := l.API.(GitHubMainFileAPI)
	if !ok {
		return false, fmt.Errorf("observe: the subscription records a scaffold contribution that created files in %s, but the GitHub API does not implement observe.GitHubMainFileAPI, so the lister cannot read whether main still holds them", repo)
	}
	for _, f := range files {
		content, held, err := api.MainFile(ctx, d, repo, f.Path)
		if err != nil {
			return false, fmt.Errorf("observe: read %s on main of %s: %w", f.Path, repo, err)
		}
		if held && gitBlobHash(content) == f.Blob {
			return true, nil
		}
	}
	return false, nil
}

// gitBlobHash is the hash git gives content as a blob, which a landing record keeps for each file.
func gitBlobHash(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func (l GitHubTagged) onRepository(ctx context.Context, d contract.Desired, repo, subscriptionID string, carriesTopic bool) ([]Tagged, error) {
	hooks, err := l.API.Webhooks(ctx, d, repo)
	if err != nil {
		return nil, err
	}
	var out []Tagged
	for _, h := range hooks {
		if carriesTopic || (l.ProcessorURL != "" && h.URL == l.ProcessorURL) {
			out = append(out, Tagged{Type: "github.repository-webhook", ID: repo + "#hooks/" + h.ID})
		}
	}
	secrets, err := l.API.ActionsSecretNames(ctx, d, repo)
	if err != nil {
		return nil, err
	}
	out = append(out, fixedNames("github.actions-secret", repo, secrets, l.Secrets)...)
	variables, err := l.API.ActionsVariableNames(ctx, d, repo)
	if err != nil {
		return nil, err
	}
	out = append(out, fixedNames("github.actions-variable", repo, variables, l.Variables)...)
	commits, err := l.API.Commits(ctx, d, repo)
	if err != nil {
		return nil, err
	}
	return append(out, l.committed(repo, commits, GitHubSubscriptionTrailer(subscriptionID))...), nil
}

func fixedNames(typ, repo string, present, fixed []string) []Tagged {
	var out []Tagged
	for _, name := range present {
		if slices.Contains(fixed, name) {
			out = append(out, Tagged{Type: typ, ID: repo + "#" + name})
		}
	}
	return out
}

func (l GitHubTagged) committed(repo string, commits []GitHubCommitPaths, trailer string) []Tagged {
	var paths []string
	for _, c := range commits {
		if carriesTrailer(c.Message, trailer) {
			paths = append(paths, c.Paths...)
		}
	}
	if l.CommitType != "" {
		if len(paths) > 0 {
			return []Tagged{{Type: l.CommitType, ID: repo}}
		}
		return nil
	}
	slices.Sort(paths)
	var out []Tagged
	for _, p := range slices.Compact(paths) {
		out = append(out, Tagged{Type: "github.workflow-file", ID: repo + "/" + p})
	}
	return out
}

// carriesTrailer reads only a closing paragraph after the subject, where git keeps trailers.
func carriesTrailer(message, trailer string) bool {
	msg := strings.TrimRight(message, "\r\n\t ")
	i := strings.LastIndex(msg, "\n\n")
	if i < 0 {
		return false
	}
	for _, line := range strings.Split(msg[i+2:], "\n") {
		if strings.TrimRight(line, "\r\t ") == trailer {
			return true
		}
	}
	return false
}

// boundRepositoryTypes are the requirement types whose bound repository a block may write to without having created it.
var boundRepositoryTypes = []string{"github.repository", "github.gitops-repository"}

// boundRepositories are the repos the requirements of a boundRepositoryTypes type bind, in reference order.
func boundRepositories(d contract.Desired) []string {
	var refs []string
	for ref, typ := range d.RequirementTypes {
		if slices.Contains(boundRepositoryTypes, typ) {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	var repos []string
	for _, ref := range refs {
		if repo := d.Bindings[ref]["repo"]; repo != "" && !slices.Contains(repos, repo) {
			repos = append(repos, repo)
		}
	}
	return repos
}
