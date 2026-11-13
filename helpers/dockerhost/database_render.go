package dockerhost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The files a Database writes in an app's directory, beside the app's own, as the host's pull agent reads them.
const (
	composeSuffix  = ".compose.yaml"
	envNamesSuffix = ".env.names"
	backupSuffix   = ".backup"
	requestSuffix  = ".backup-request"

	sizeLabel     = "eu.mitstack.size"
	agentEnvDir   = "/var/lib/mitstack-agent/env"
	labelSub      = "mitstack.dev/subscription"
	labelApp      = "mitstack.dev/app"
	restartPolicy = "unless-stopped"
	backupEvery   = "24h"
	healthRetries = 5
)

func (db Database) fileCompose() string     { return db.Block + composeSuffix }
func (db Database) fileEnvNames() string    { return db.Block + envNamesSuffix }
func (db Database) fileBackup() string      { return db.Block + backupSuffix }
func (db Database) fileRequest() string     { return db.Block + requestSuffix }
func (db Database) fileReservation() string { return ReservationDir + "/" + db.Block + ".json" }
func (db Database) volume() string          { return db.Engine.Service + "-data" }

func (db Database) ownedFiles() []string {
	return []string{db.fileCompose(), db.fileEnvNames(), db.fileBackup(), db.fileRequest(), db.fileReservation()}
}

// landedFiles are the files whose presence at main decides the database's state.
func (db Database) landedFiles() []string {
	return []string{db.fileCompose(), db.fileEnvNames(), db.fileBackup(), db.fileReservation()}
}

func appPath(app, name string) string { return app + "/" + name }

// Compose is the compose file a Database writes, which a block's test reads back.
type Compose struct {
	Services map[string]ComposeService `json:"services"`
	Networks map[string]composeNetwork `json:"networks"`
	Volumes  map[string]ComposeVolume  `json:"volumes"`
}

// ComposeService is the database container's service in a Compose.
type ComposeService struct {
	Image         string            `json:"image"`
	ContainerName string            `json:"container_name"`
	Restart       string            `json:"restart"`
	CPUs          json.Number       `json:"cpus"`
	MemLimit      int64             `json:"mem_limit"`
	MemswapLimit  int64             `json:"memswap_limit"`
	Entrypoint    []string          `json:"entrypoint"`
	Command       []string          `json:"command"`
	EnvFile       []composeEnvFile  `json:"env_file"`
	Environment   map[string]string `json:"environment,omitempty"`
	Volumes       []string          `json:"volumes"`
	Healthcheck   ComposeHealth     `json:"healthcheck"`
	SecurityOpt   []string          `json:"security_opt"`
	Labels        map[string]string `json:"labels"`
	Networks      []string          `json:"networks"`
	Ports         []string          `json:"ports,omitempty"`
}

type composeEnvFile struct {
	Path     string `json:"path"`
	Required bool   `json:"required"`
}

// ComposeHealth is a ComposeService's health check.
type ComposeHealth struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval"`
	Timeout     string   `json:"timeout"`
	Retries     int      `json:"retries"`
	StartPeriod string   `json:"start_period"`
}

type composeNetwork struct {
	Name string `json:"name"`
}

// ComposeVolume's size label holds the volume to the disk the database reserves.
type ComposeVolume struct {
	Labels map[string]string `json:"labels"`
}

// Network is the name of the app's own network.
func (c Compose) Network(name string) string { return c.Networks[name].Name }

// renderCompose writes JSON, which Compose reads as YAML, so no value needs quoting by hand; "$$" is Compose's "$".
func (db Database) renderCompose(r request) (string, error) {
	e := db.Engine
	var entrypoint []string
	for _, word := range e.Entrypoint {
		entrypoint = append(entrypoint, strings.ReplaceAll(word, "$", "$$"))
	}
	c := Compose{
		Services: map[string]ComposeService{e.Service: {
			Image:         e.Image,
			ContainerName: db.container(r.target),
			Restart:       restartPolicy,
			CPUs:          json.Number(strconv.FormatFloat(float64(r.size.CPUMillis)/millisPerCPU, 'f', -1, 64)),
			MemLimit:      r.size.MemoryBytes,
			MemswapLimit:  r.size.MemoryBytes,
			Entrypoint:    entrypoint,
			Command:       e.Command,
			EnvFile:       []composeEnvFile{{Path: agentEnvDir + "/" + r.app + "." + db.Block + ".env", Required: true}},
			Environment:   e.Environment,
			Volumes:       []string{db.volume() + ":" + e.DataDir},
			Healthcheck: ComposeHealth{
				Test: e.Healthcheck, Interval: "30s", Timeout: "5s", Retries: healthRetries, StartPeriod: "60s",
			},
			SecurityOpt: []string{"no-new-privileges:true"},
			Labels:      map[string]string{labelSub: r.subID, labelApp: r.app},
			Networks:    []string{"default"},
		}},
		Networks: map[string]composeNetwork{"default": {Name: "app-" + r.app}},
		Volumes:  map[string]ComposeVolume{db.volume(): {Labels: map[string]string{sizeLabel: sizeText(r.size.DiskBytes)}}},
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("rendering %s's database compose file: %w", r.app, err)
	}
	return string(out) + "\n", nil
}

// sizeText writes bytes as the whole GiB or MiB the agent's size label takes.
func sizeText(bytes int64) string {
	if mib := bytes / bytesPerMiB; mib%mibPerGiB == 0 {
		return strconv.FormatInt(mib/mibPerGiB, 10) + "GiB"
	}
	return strconv.FormatInt(bytes/bytesPerMiB, 10) + "MiB"
}

// outputs are the connection the database hands its app's other blocks, the password named by the secret holding it.
func (db Database) outputs(t target) map[string]string {
	return map[string]string{
		"host": db.container(t), "port": db.Engine.Port, "dbName": DatabaseName, "username": DatabaseUser,
		"passwordSecret": db.passwordSource(),
	}
}

// passwordSource is the host's source line for the password it generates once and keeps in the block's own folder.
func (db Database) passwordSource() string { return "generated:" + db.Block + ":" + passwordSecret }

// renderEnvNames fills the database container's password from the host's generated secret.
func (db Database) renderEnvNames() string {
	return PasswordVariable + "=" + db.passwordSource() + "\n"
}

func (db Database) renderBackup() string {
	return "service=" + db.Engine.Service + "\ncommand=" + db.Engine.Dump + "\nrestore=" + db.Engine.Restore + "\nevery=" + backupEvery + "\n"
}

// renderFiles is every file a Database writes in the app's directory, the backup request aside.
func (db Database) renderFiles(r request) (map[string]string, error) {
	composeText, err := db.renderCompose(r)
	if err != nil {
		return nil, err
	}
	reservation, err := json.Marshal(r.size)
	if err != nil {
		return nil, fmt.Errorf("rendering %s's database reservation: %w", r.app, err)
	}
	return map[string]string{
		appPath(r.app, db.fileCompose()):     composeText,
		appPath(r.app, db.fileEnvNames()):    db.renderEnvNames(),
		appPath(r.app, db.fileBackup()):      db.renderBackup(),
		appPath(r.app, db.fileReservation()): string(reservation) + "\n",
	}, nil
}
