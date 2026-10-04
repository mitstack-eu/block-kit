package contract

import (
	"cmp"
	"slices"
)

// The resource registry is the one list of output names a block may declare
// in spec.outputs and read under spec.requires[].reads. The platform reads it
// from this package, as it reads LifecycleHookPoints, and blockverify checks
// a block's manifest against it, so neither side keeps a copy of the names.

// ResourceEntry is one entry of the resource registry: an output name a block
// declares in spec.outputs, and what a value published under that name holds.
type ResourceEntry struct {
	// Name is the output name as a manifest declares it.
	Name string
	// Kind is the group the entry belongs to, as the label the console
	// shows: App, Host & network, Data, Build & deploy, Access, Model,
	// Coding agent, Work or Source.
	Kind string
	// Meaning says in one sentence what the value is.
	Meaning string
	// Shape says what form the value takes.
	Shape string
	// Example is a sample value. Every entry carries one except a secret
	// entry, which carries none because it would show the secret.
	Example string
	// Secret marks an output whose value is sensitive. For a name a kind
	// declares, it equals the mark the kind gives the output.
	Secret bool
	// Aliases are other names that resolve to this entry. It is never nil,
	// and empty when the entry answers to its name alone.
	Aliases []string
}

// clone returns a copy of r whose Aliases share no backing array with r's. An
// empty list stays empty rather than becoming nil.
func (r ResourceEntry) clone() ResourceEntry {
	r.Aliases = append([]string{}, r.Aliases...)
	return r
}

// Resources returns every registry entry, sorted by name. Each call returns
// fresh copies, so a caller may change the result without changing the
// registry.
func Resources() []ResourceEntry {
	out := make([]ResourceEntry, len(resourceRegistry))
	for i, r := range resourceRegistry {
		out[i] = r.clone()
	}
	slices.SortFunc(out, func(a, b ResourceEntry) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// LookupResource returns a copy of the entry named name, or of the entry that
// lists name among its aliases. The match is case-sensitive.
func LookupResource(name string) (ResourceEntry, bool) {
	for _, r := range resourceRegistry {
		if r.Name == name || slices.Contains(r.Aliases, name) {
			return r.clone(), true
		}
	}
	return ResourceEntry{}, false
}

// NearestResource returns the registry entry name or alias with the smallest
// edit distance from name, the alphabetically first on a tie. A registered
// name or alias is its own nearest.
func NearestResource(name string) string {
	best, bestDist := "", -1
	for _, r := range resourceRegistry {
		for _, candidate := range append([]string{r.Name}, r.Aliases...) {
			d := levenshtein(name, candidate)
			if bestDist < 0 || d < bestDist || d == bestDist && candidate < best {
				best, bestDist = candidate, d
			}
		}
	}
	return best
}

// levenshtein returns the number of single-character insertions, deletions
// and substitutions that turn a into b.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// resourceRegistry holds every output name the platform's pinned blocks
// declare in their manifests and every output a kind names, under its current
// name, with any older name as an alias.
var resourceRegistry = []ResourceEntry{
	{
		Name:    "agentEndpoint",
		Kind:    "Coding agent",
		Meaning: "The page where the coding agent's workflow runs in the app's repository.",
		Shape:   "https URL",
		Example: "https://github.com/acme/shop/actions/workflows/claude-agent.yml",
		Aliases: []string{},
	},
	{
		Name:    "apiEndpoint",
		Kind:    "Host & network",
		Meaning: "The address of the cluster's Kubernetes API.",
		Shape:   "https URL of a host and port",
		Example: "https://203.0.113.10:6443",
		Aliases: []string{},
	},
	{
		Name:    "appHostname",
		Kind:    "Host & network",
		Meaning: "The DNS name the app is served on.",
		Shape:   "hostname",
		Example: "shop.apps.mitstack.dev",
		Aliases: []string{},
	},
	{
		Name:    "appName",
		Kind:    "App",
		Meaning: "The name of the app the block acted for.",
		Shape:   "app name, lowercase letters, digits and hyphens",
		Example: "shop",
		Aliases: []string{},
	},
	{
		Name:    "appUrl",
		Kind:    "Host & network",
		Meaning: "The public HTTPS address the app answers on.",
		Shape:   "https URL",
		Example: "https://shop.apps.mitstack.dev",
		Aliases: []string{},
	},
	{
		Name:    "artifact",
		Kind:    "Build & deploy",
		Meaning: "Whether the app's build pipeline is registered to publish its image.",
		Shape:   "the word registered",
		Example: "registered",
		Aliases: []string{},
	},
	{
		Name:    "awsCredential",
		Kind:    "Access",
		Meaning: "How a job on the runner obtains AWS credentials: from the runner's own instance profile, or by assuming a federated role.",
		Shape:   "one of ambient or federated",
		Example: "federated",
		Aliases: []string{},
	},
	{
		Name:    "boardRepo",
		Kind:    "Work",
		Meaning: "The repository holding the board's check workflows that the app's pull requests call.",
		Shape:   "GitHub repository as owner/name",
		Example: "acme/shop-board",
		Aliases: []string{},
	},
	{
		Name:    "buildRequestId",
		Kind:    "Coding agent",
		Meaning: "The build request whose coding session generated the source.",
		Shape:   "UUID",
		Example: "5d6f1f0e-2b7a-4c1e-9a53-8f0c2d4b6e71",
		Aliases: []string{},
	},
	{
		Name:    "caCertificate",
		Kind:    "Access",
		Meaning: "The certificate authority that signs the serving certificate of the cluster's Kubernetes API.",
		Shape:   "PEM-encoded X.509 certificate",
		Example: "-----BEGIN CERTIFICATE-----\nMIIBdzCCAR2gAwIBAgIBADAKBggqhkjOPQQDAjAjMSEwHwYDVQQDDBhrM3Mtc2Vy\n-----END CERTIFICATE-----",
		Aliases: []string{},
	},
	{
		Name:    "cdRepo",
		Kind:    "Build & deploy",
		Meaning: "The repository holding the app's deploy pipeline.",
		Shape:   "GitHub repository as owner/name",
		Example: "acme/shop-cd",
		Aliases: []string{},
	},
	{
		Name:    "certTrusted",
		Kind:    "Host & network",
		Meaning: "Whether the app's TLS certificate is trusted by a browser.",
		Shape:   "true or false",
		Example: "true",
		Aliases: []string{},
	},
	{
		Name:    "ciCheckContribution",
		Kind:    "Work",
		Meaning: "The board checks the app's CI pipeline runs, as JSON.",
		Shape:   "JSON object with a checks list of name, uses and when",
		Example: "{\"checks\":[{\"name\":\"board-keys\",\"uses\":\"acme/shop-board/.github/workflows/keys.yml@main\",\"when\":\"pull_request\"}]}",
		Aliases: []string{},
	},
	{
		Name:    "ciRepo",
		Kind:    "Build & deploy",
		Meaning: "The repository holding the app's build pipeline.",
		Shape:   "GitHub repository as owner/name",
		Example: "acme/shop-ci",
		Aliases: []string{},
	},
	{
		Name:    "claim",
		Kind:    "Host & network",
		Meaning: "The name of the Crossplane claim that keeps the app's resources converged.",
		Shape:   "Kubernetes object name starting rc-",
		Example: "rc-5d6f1f0e-2b7a-4c1e-9a53-8f0c2d4b6e71",
		Aliases: []string{},
	},
	{
		Name:    "cloud.region",
		Kind:    "Host & network",
		Meaning: "The cloud region the app runs in.",
		Shape:   "cloud region code",
		Example: "eu-central-1",
		Aliases: []string{"region"},
	},
	{
		Name:    "clusterCredential",
		Kind:    "Access",
		Meaning: "The credential a client presents to the cluster's Kubernetes API.",
		Shape:   "Kubernetes bearer token",
		Secret:  true,
		Aliases: []string{},
	},
	{
		Name:    "clusterRepoUrl",
		Kind:    "Build & deploy",
		Meaning: "The web address of the private repository Argo CD syncs the app's cluster from.",
		Shape:   "https URL",
		Example: "https://github.com/acme/orbitdemo-cluster",
		Aliases: []string{},
	},
	{
		Name:    "dataSnapshotId",
		Kind:    "Data",
		Meaning: "The snapshot of the app's data volume, handed to the next host during a stack switch.",
		Shape:   "EBS snapshot id, empty when no switch is under way",
		Example: "snap-0a1b2c3d4e5f67890",
		Aliases: []string{},
	},
	{
		Name:    "dbName",
		Kind:    "Data",
		Meaning: "The name of the app's database.",
		Shape:   "Postgres identifier",
		Example: "shop_db",
		Aliases: []string{},
	},
	{
		Name:    "deployRoleArn",
		Kind:    "Access",
		Meaning: "The role the app's deploy pipeline assumes in the connected cloud account.",
		Shape:   "AWS IAM role ARN",
		Secret:  true,
		Aliases: []string{},
	},
	{
		Name:    "deployment",
		Kind:    "Build & deploy",
		Meaning: "Identifies the app's registered deploy pipeline.",
		Shape:   "UUID",
		Example: "9b2e4c1a-7d3f-4e8b-a5c6-1f0d2e3b4a59",
		Aliases: []string{},
	},
	{
		Name:    "driftPolicy",
		Kind:    "Host & network",
		Meaning: "What the reconciler does when a resource drifts from its declared state.",
		Shape:   "one of revert or report",
		Example: "revert",
		Aliases: []string{},
	},
	{
		Name:    "driftedResources",
		Kind:    "Host & network",
		Meaning: "The resources found drifted from their declared state.",
		Shape:   "comma-separated resource names, empty when none drifted",
		Example: "bucket,queue",
		Aliases: []string{},
	},
	{
		Name:    "dshSessionUrl",
		Kind:    "Coding agent",
		Meaning: "Where a DeepSeek harness session for the app is opened.",
		Shape:   "https URL",
		Example: "https://github.com/acme/shop",
		Aliases: []string{},
	},
	{
		Name:    "ecrPushRoleArn",
		Kind:    "Access",
		Meaning: "The role a build job on another runner assumes to push to the app's own image registry.",
		Shape:   "AWS IAM role ARN, empty until the app's repository is known",
		Example: "arn:aws:iam::123456789012:role/mitstack-ecr-push-shop",
		Aliases: []string{},
	},
	{
		Name:    "ecrRegistry",
		Kind:    "Build & deploy",
		Meaning: "The container registry the app's images are pushed to and pulled from.",
		Shape:   "registry hostname",
		Example: "123456789012.dkr.ecr.eu-central-1.amazonaws.com",
		Aliases: []string{},
	},
	{
		Name:    "enrolmentTokenSha256",
		Kind:    "Access",
		Meaning: "The SHA-256 digest of the token the cluster's node presents to enrol with the platform; the token itself is never published.",
		Shape:   "52 lowercase base32 characters",
		Example: "abcdefghabcdefghabcdefghabcdefghabcdefghabcdefghabcd",
		Aliases: []string{},
	},
	{
		Name:    "execKind",
		Kind:    "Host & network",
		Meaning: "How the platform runs commands on the app's host.",
		Shape:   "mechanism name",
		Example: "aws-ssm",
		Aliases: []string{},
	},
	{
		Name:    "execTarget",
		Kind:    "Host & network",
		Meaning: "The host that commands are sent to.",
		Shape:   "host id",
		Example: "i-0abc123def4567890",
		Aliases: []string{},
	},
	{
		Name:    "host",
		Kind:    "Data",
		Meaning: "The address of the app's database server.",
		Shape:   "hostname",
		Example: "i-0abc123def4567890.internal",
		Aliases: []string{},
	},
	{
		Name:    "host.type",
		Kind:    "Host & network",
		Meaning: "The machine type the app runs on.",
		Shape:   "cloud instance type",
		Example: "t3.small",
		Aliases: []string{"instanceType"},
	},
	{
		Name:    "hostId",
		Kind:    "Host & network",
		Meaning: "The id of the host the app runs on.",
		Shape:   "host id",
		Example: "i-0abc123def4567890",
		Aliases: []string{},
	},
	{
		Name:    "hostProvider",
		Kind:    "Host & network",
		Meaning: "The provider of the host the app runs on.",
		Shape:   "provider name",
		Example: "aws",
		Aliases: []string{},
	},
	{
		Name:    "imageRepo",
		Kind:    "Build & deploy",
		Meaning: "The container registry the app's build pipeline pushes its image to.",
		Shape:   "registry hostname",
		Example: "123456789012.dkr.ecr.eu-central-1.amazonaws.com",
		Aliases: []string{},
	},
	{
		Name:    "instanceId",
		Kind:    "Host & network",
		Meaning: "The id of the EC2 instance the app runs on.",
		Shape:   "EC2 instance id",
		Example: "i-0abc123def4567890",
		Aliases: []string{},
	},
	{
		Name:    "issuerBucket",
		Kind:    "Access",
		Meaning: "The name of the bucket that hosts the cluster's service-account issuer.",
		Shape:   "S3 bucket name, mitstack-oidc- and 26 lowercase base32 characters",
		Example: "mitstack-oidc-abcdefghabcdefghabcdefghab",
		Aliases: []string{},
	},
	{
		Name:    "issuerUrl",
		Kind:    "Access",
		Meaning: "The service-account issuer the cluster names in the tokens it signs, served from the issuer bucket.",
		Shape:   "https URL",
		Example: "https://mitstack-oidc-abcdefghabcdefghabcdefghab.s3.eu-central-1.amazonaws.com/abcd2345-6f7a-4b2c-9d3e-5f6a7b2c3d4e",
		Aliases: []string{},
	},
	{
		Name:    "landedBranch",
		Kind:    "Source",
		Meaning: "The branch the generated source landed on.",
		Shape:   "git branch name",
		Example: "main",
		Aliases: []string{},
	},
	{
		Name:    "landedBuildRequestId",
		Kind:    "Coding agent",
		Meaning: "The build request whose generated source was landed.",
		Shape:   "UUID",
		Example: "5d6f1f0e-2b7a-4c1e-9a53-8f0c2d4b6e71",
		Aliases: []string{},
	},
	{
		Name:    "landedSha",
		Kind:    "Source",
		Meaning: "The source commit the landing carries.",
		Shape:   "40-character git commit sha",
		Example: "3f9c2b1e8d7a6c5b4a39281706f5e4d3c2b1a098",
		Aliases: []string{},
	},
	{
		Name:    "landingPullRequest",
		Kind:    "Source",
		Meaning: "The pull request that landed the generated source.",
		Shape:   "https URL of a pull request",
		Example: "https://github.com/acme/shop/pull/12",
		Aliases: []string{},
	},
	{
		Name:    "llmEndpoint",
		Kind:    "Model",
		Meaning: "The OpenAI-compatible endpoint serving the model.",
		Shape:   "https URL",
		Example: "https://llm-0123456789ab.eu-central-1.vllm.internal/v1",
		Aliases: []string{},
	},
	{
		Name:    "model",
		Kind:    "Model",
		Meaning: "The model being served.",
		Shape:   "model id",
		Example: "mistralai/Mistral-7B-Instruct-v0.3",
		Aliases: []string{},
	},
	{
		Name:    "namespace",
		Kind:    "Host & network",
		Meaning: "The Kubernetes namespace the app's resources on a cluster are placed in.",
		Shape:   "Kubernetes namespace name, lowercase letters, digits and hyphens",
		Example: "app-shop",
		Aliases: []string{},
	},
	{
		Name:    "oidcProviderArn",
		Kind:    "Access",
		Meaning: "The GitHub OIDC provider that the deploy role trusts in the connected cloud account.",
		Shape:   "AWS IAM OIDC provider ARN",
		Secret:  true,
		Aliases: []string{},
	},
	{
		Name:    "opencodeUrl",
		Kind:    "Coding agent",
		Meaning: "Where the OpenCode agent is served.",
		Shape:   "https URL",
		Example: "https://shop-0123456789ab.eu-central-1.opencode.internal",
		Aliases: []string{},
	},
	{
		Name:    "password",
		Kind:    "Data",
		Meaning: "The password the database user connects with.",
		Shape:   "password string",
		Secret:  true,
		Aliases: []string{},
	},
	{
		Name:    "port",
		Kind:    "Data",
		Meaning: "The port the app's database listens on.",
		Shape:   "TCP port number",
		Example: "5432",
		Aliases: []string{},
	},
	{
		Name:    "privateEndpoint",
		Kind:    "Host & network",
		Meaning: "The address of the cluster's Kubernetes API on the cluster's private network.",
		Shape:   "https URL of a private IP address and port",
		Example: "https://10.255.0.2:6443",
		Aliases: []string{},
	},
	{
		Name:    "publicIp",
		Kind:    "Host & network",
		Meaning: "The public IP address of the app's host.",
		Shape:   "IPv4 address",
		Example: "203.0.113.10",
		Aliases: []string{},
	},
	{
		Name:    "pullRequestUrl",
		Kind:    "Source",
		Meaning: "The web address of the pull request.",
		Shape:   "https URL of a pull request",
		Example: "https://github.com/acme/shop/pull/1",
		Aliases: []string{},
	},
	{
		Name:    "reachable",
		Kind:    "Host & network",
		Meaning: "Whether the app answers on its public address.",
		Shape:   "true or false",
		Example: "true",
		Aliases: []string{},
	},
	{
		Name:    "reconciledResources",
		Kind:    "Host & network",
		Meaning: "How many resources the reconciler keeps converged.",
		Shape:   "non-negative integer",
		Example: "3",
		Aliases: []string{},
	},
	{
		Name:    "registryHost",
		Kind:    "Build & deploy",
		Meaning: "The container registry the app's images are pushed to and pulled from.",
		Shape:   "registry hostname",
		Example: "123456789012.dkr.ecr.eu-central-1.amazonaws.com",
		Aliases: []string{},
	},
	{
		Name:    "registryKind",
		Kind:    "Build & deploy",
		Meaning: "The kind of container registry the app's images live in.",
		Shape:   "registry kind name",
		Example: "aws-ecr",
		Aliases: []string{},
	},
	{
		Name:    "repo",
		Kind:    "Source",
		Meaning: "The app's source repository.",
		Shape:   "GitHub repository as owner/name",
		Example: "acme/shop",
		Aliases: []string{},
	},
	{
		Name:    "repoUrl",
		Kind:    "Source",
		Meaning: "The web address of the app's source repository.",
		Shape:   "https URL",
		Example: "https://github.com/acme/shop",
		Aliases: []string{},
	},
	{
		Name:    "roleArn",
		Kind:    "Access",
		Meaning: "The ARN of the IAM role.",
		Shape:   "AWS IAM role ARN",
		Secret:  true,
		Aliases: []string{},
	},
	{
		Name:    "runnerLabel",
		Kind:    "Build & deploy",
		Meaning: "The label of the runner the app's CI jobs run on.",
		Shape:   "runner label",
		Example: "mitstack-app-shop",
		Aliases: []string{},
	},
	{
		Name:    "runsOn",
		Kind:    "Build & deploy",
		Meaning: "The runs-on value for the app's delivery jobs.",
		Shape:   "JSON array of runner labels",
		Example: "[\"self-hosted\",\"mitstack-app-shop\"]",
		Aliases: []string{},
	},
	{
		Name:    "runsOnPr",
		Kind:    "Build & deploy",
		Meaning: "The runs-on value for the app's pull-request jobs.",
		Shape:   "JSON array of runner labels",
		Example: "[\"self-hosted\",\"mitstack-app-shop-pr\"]",
		Aliases: []string{},
	},
	{
		Name:    "scaffoldCommit",
		Kind:    "Source",
		Meaning: "The commit on main that carries the app's scaffold.",
		Shape:   "40-character git commit sha",
		Example: "3f9c2b1e8d7a6c5b4a39281706f5e4d3c2b1a098",
		Aliases: []string{},
	},
	{
		Name:    "scaffoldContribution",
		Kind:    "Source",
		Meaning: "One block's files, environment keys and config entries for the app's repository, as JSON.",
		Shape:   "JSON object with files, env and config maps",
		Example: "{\"files\":{\".github/workflows/build.yml\":\"name: build\"},\"env\":{\"PORT\":\"8080\"}}",
		Aliases: []string{},
	},
	{
		Name:    "scaffoldPullRequest",
		Kind:    "Source",
		Meaning: "The pull request carrying the app's scaffold when main refused a direct commit.",
		Shape:   "https URL of a pull request",
		Example: "https://github.com/acme/shop/pull/1",
		Aliases: []string{},
	},
	{
		Name:    "serverId",
		Kind:    "Host & network",
		Meaning: "The id of the Hetzner Cloud server the cluster runs on.",
		Shape:   "Hetzner Cloud server id, a positive integer",
		Example: "62301001",
		Aliases: []string{},
	},
	{
		Name:    "serviceAccount",
		Kind:    "Access",
		Meaning: "The Kubernetes service account the app's workloads in its namespace run as.",
		Shape:   "Kubernetes object name",
		Example: "shop",
		Aliases: []string{},
	},
	{
		Name:    "settingsWarning",
		Kind:    "Source",
		Meaning: "Names a repository setting the scaffold could not reconcile.",
		Shape:   "sentence, absent when every setting is reconciled",
		Example: "enforce_admins is not reconciled",
		Aliases: []string{},
	},
	{
		Name:    "sourceBranch",
		Kind:    "Source",
		Meaning: "The branch holding the source a coding session generated.",
		Shape:   "git branch name",
		Example: "build/add-login-page",
		Aliases: []string{},
	},
	{
		Name:    "sourceSha",
		Kind:    "Source",
		Meaning: "The commit holding the source a coding session generated.",
		Shape:   "40-character git commit sha",
		Example: "3f9c2b1e8d7a6c5b4a39281706f5e4d3c2b1a098",
		Aliases: []string{},
	},
	{
		Name:    "standardPullRequest",
		Kind:    "Coding agent",
		Meaning: "The pull request that lands the coding agent's standard files in the app's repository.",
		Shape:   "https URL of a pull request",
		Example: "https://github.com/acme/shop/pull/2",
		Aliases: []string{},
	},
	{
		Name:    "url",
		Kind:    "Work",
		Meaning: "The address the Plain issue tracker is served on.",
		Shape:   "https URL",
		Example: "https://tracker.apps.mitstack.dev",
		Aliases: []string{},
	},
	{
		Name:    "username",
		Kind:    "Data",
		Meaning: "The database user the app connects as.",
		Shape:   "Postgres identifier",
		Example: "app",
		Aliases: []string{},
	},
	{
		Name:    "workflowPath",
		Kind:    "Coding agent",
		Meaning: "The path of the coding agent's workflow file in the app's repository.",
		Shape:   "repository-relative file path",
		Example: ".github/workflows/claude-agent.yml",
		Aliases: []string{},
	},
	{
		Name:    "workflowUrl",
		Kind:    "Build & deploy",
		Meaning: "The web address of the app's deploy workflow file.",
		Shape:   "https URL",
		Example: "https://github.com/acme/shop/blob/main/.github/workflows/mitstack-aws-deploy.yml",
		Aliases: []string{},
	},
}
