package dockerhost

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

// DatabaseResource is the one resource a Database block declares.
const DatabaseResource = "database"

const (
	reqHost     = "host"
	paramApp    = "appName"
	paramCPU    = "dbCpu"
	paramMemory = "dbMemory"
	paramDisk   = "dbDisk"
	paramEnv    = "env"

	defaultCPU    = "0.5"
	defaultMemory = "512Mi"
	defaultDisk   = "1Gi"

	databaseName = "app"
	databaseUser = "app"
)

// Database is a per-app block that runs one app's database on its org's Docker host, lands it only on a host with room,
// and removes it only once its final backup is uploaded.
type Database struct {
	// Block is the block's id, which names its files and labels its volume and backups on the host.
	Block   string
	Type    string
	Engine  Engine
	Reports observe.NodeReportsAPI
	// GitHub makes the client of the host's repository, nil for one that waits 30 seconds at most.
	GitHub func(base, token string) *ghapi.Client
	Now    func() time.Time
}

// Engine is what a Database takes from its database engine; the rest is the same for every engine.
type Engine struct {
	// Service names the compose service, the container <app>-<Service> and its volume.
	Service string
	// Image is pinned by digest.
	Image   string
	Port    string
	DataDir string
	// Entrypoint is written with plain "$" signs, which the kit escapes for Compose.
	Entrypoint  func(Names) []string
	Command     []string
	Environment map[string]string
	Healthcheck []string
	// Dump writes the database to its output and Restore loads a dump from its input, each one shell command line.
	Dump, Restore func(Names) string
	Trouble       *regexp.Regexp
}

// Names maps each value a database gives, such as password, to the variable the app reads it under.
type Names map[string]string

var values = []struct{ name, variable string }{
	{"host", "DB_HOST"}, {"port", "DB_PORT"}, {"dbName", "DB_NAME"}, {"username", "DB_USER"}, {"password", "DB_PASSWORD"},
}

var (
	// appRe is the directory name the host's pull agent accepts.
	appRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	subIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	varRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func (db Database) now() time.Time {
	if db.Now == nil {
		return time.Now()
	}
	return db.Now()
}

type target struct {
	subID, app, hostSub string
}

type request struct {
	target
	size  Reservation
	names Names
}

func readTarget(d contract.Desired) (target, error) {
	t := target{subID: d.SubscriptionID, app: d.Params[paramApp], hostSub: d.Bindings[reqHost]["subscriptionId"]}
	if !subIDRe.MatchString(t.subID) {
		return target{}, handler.Refuse("invalid-subscription", "the subscription id %q is not letters, digits, underscores and hyphens", t.subID)
	}
	if err := handler.NeedsAppName(d); err != nil {
		return target{}, err
	}
	if !appRe.MatchString(t.app) {
		return target{}, handler.Refuse("invalid-param",
			`params["%s"] is %q; the host runs an app only from a directory of lowercase letters, digits and hyphens`, paramApp, t.app)
	}
	return t, nil
}

func readRequest(d contract.Desired) (request, error) {
	t, err := readTarget(d)
	if err != nil {
		return request{}, err
	}
	var size Reservation
	if size.CPUMillis, err = readParam(d.Params, paramCPU, defaultCPU, ParseCPU); err != nil {
		return request{}, err
	}
	if size.MemoryBytes, err = readParam(d.Params, paramMemory, defaultMemory, ParseSize); err != nil {
		return request{}, err
	}
	if size.DiskBytes, err = readParam(d.Params, paramDisk, defaultDisk, ParseSize); err != nil {
		return request{}, err
	}
	names, err := readNames(d.Params[paramEnv])
	if err != nil {
		return request{}, err
	}
	return request{target: t, size: size, names: names}, nil
}

func readParam(params map[string]string, name, fallback string, parse func(string) (int64, error)) (int64, error) {
	v := strings.TrimSpace(params[name])
	if v == "" {
		v = fallback
	}
	n, err := parse(v)
	if err != nil {
		return 0, handler.Refuse("invalid-param", `params["%s"] is %q; %v`, name, v, err)
	}
	return n, nil
}

// readNames reads the env input's NAME=value entries over the default names, refusing one it cannot give.
func readNames(env string) (Names, error) {
	names := Names{}
	for _, v := range values {
		names[v.name] = v.variable
	}
	var problems []string
	for _, entry := range strings.Split(env, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		variable, value, ok := strings.Cut(entry, "=")
		variable, value = strings.TrimSpace(variable), strings.TrimSpace(value)
		switch {
		case !ok || !varRe.MatchString(variable):
			problems = append(problems, fmt.Sprintf("%q is not NAME=value with NAME a variable name", entry))
		case names[value] == "":
			problems = append(problems, fmt.Sprintf("%q names %q, which is not one of host, port, dbName, username and password", entry, value))
		default:
			names[value] = variable
		}
	}
	seen := map[string]string{}
	for _, v := range values {
		if other, taken := seen[names[v.name]]; taken {
			problems = append(problems, fmt.Sprintf("%s and %s would both be %s", other, v.name, names[v.name]))
		}
		seen[names[v.name]] = v.name
	}
	if len(problems) > 0 {
		return nil, handler.Refuse("invalid-param", `params["%s"] cannot name the database's variables: %s`, paramEnv, strings.Join(problems, "; "))
	}
	return names, nil
}

func (r request) variables() []string {
	var out []string
	for _, v := range r.names {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func (db Database) container(t target) string { return t.app + "-" + db.Engine.Service }

func (db Database) identity(repo *hostRepo, t target) map[string]string {
	return map[string]string{"repo": repo.fullName, "app": t.app}
}

func (db Database) dial(d contract.Desired) (*hostRepo, error) {
	return dialHostRepo(d, d.RecordedResources[DatabaseResource].Identity["repo"], db.GitHub)
}
