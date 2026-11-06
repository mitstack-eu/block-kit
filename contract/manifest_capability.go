package contract

// ManifestPresentation is how the console draws a capability: its vendor, the category the Capabilities page groups it under, and its mark.
type ManifestPresentation struct {
	Vendor   string              `json:"vendor"`
	Category string              `json:"category"`
	Mark     []ManifestMarkShape `json:"mark,omitempty"`
}

// ManifestMarkShape holds exactly one shape, each attribute a number or a list of numbers except a path's d.
type ManifestMarkShape struct {
	Path     *MarkPath    `json:"path,omitempty"`
	Circle   *MarkCircle  `json:"circle,omitempty"`
	Rect     *MarkRect    `json:"rect,omitempty"`
	Line     *MarkLine    `json:"line,omitempty"`
	Polyline *MarkPoints  `json:"polyline,omitempty"`
	Polygon  *MarkPoints  `json:"polygon,omitempty"`
	Ellipse  *MarkEllipse `json:"ellipse,omitempty"`
}

type MarkPath struct {
	D string `json:"d"`
}

type MarkCircle struct {
	Cx float64 `json:"cx"`
	Cy float64 `json:"cy"`
	R  float64 `json:"r"`
}

type MarkRect struct {
	X      float64  `json:"x"`
	Y      float64  `json:"y"`
	Width  float64  `json:"width"`
	Height float64  `json:"height"`
	Rx     *float64 `json:"rx,omitempty"`
	Ry     *float64 `json:"ry,omitempty"`
}

type MarkLine struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// MarkPoints holds the coordinates of a polyline or a polygon as x, y pairs in one list.
type MarkPoints struct {
	Points []float64 `json:"points"`
}

type MarkEllipse struct {
	Cx float64 `json:"cx"`
	Cy float64 `json:"cy"`
	Rx float64 `json:"rx"`
	Ry float64 `json:"ry"`
}

// The three ways an org's connection to a capability is made.
const (
	MechanismOIDC        = "oidc"
	MechanismOAuthApp    = "oauth-app"
	MechanismStoredToken = "stored-token"
)

// ConnectionMechanisms lists the mechanisms a connection section may name.
var ConnectionMechanisms = []string{MechanismOIDC, MechanismOAuthApp, MechanismStoredToken}

// ManifestConnection says how an org connects to a capability.
type ManifestConnection struct {
	Mechanism string `json:"mechanism"`
	// TokenSchema lists the fields of a stored-token credential.
	TokenSchema []ManifestTokenField `json:"tokenSchema,omitempty"`
	// Audience is what an oidc connection's token is minted for.
	Audience string `json:"audience,omitempty"`
	// InstallHint tells a person connecting an oauth-app where to install the app.
	InstallHint string `json:"installHint,omitempty"`
	// HeldOnlyByGrant means only the org's OAuth grant holds the capability, so a stored token for it holds nothing.
	HeldOnlyByGrant bool `json:"heldOnlyByGrant,omitempty"`
	// Driver names the platform code that serves the connection when a capability's operator does not.
	Driver        string                    `json:"driver,omitempty"`
	ObjectStorage *ManifestObjectStorageKey `json:"objectStorage,omitempty"`
}

// ManifestTokenField is one field of a stored-token credential.
type ManifestTokenField struct {
	Name        string `json:"name"`
	Secret      bool   `json:"secret"`
	Description string `json:"description,omitempty"`
	// AllowedValues, when set, is the fixed set of values the field may take.
	AllowedValues []string `json:"allowedValues,omitempty"`
	// OptionalGroup makes the field optional, given together with its group's other fields or not at all.
	OptionalGroup string `json:"optionalGroup,omitempty"`
}

// ManifestObjectStorageKey names the two fields of an S3 key pair held in the credential and the endpoint of a location, which {location} stands for.
type ManifestObjectStorageKey struct {
	AccessKeyField   string `json:"accessKeyField"`
	SecretKeyField   string `json:"secretKeyField"`
	EndpointTemplate string `json:"endpointTemplate"`
}

// ManifestReach names the capability whose connection reaches this one.
type ManifestReach struct {
	Capability string `json:"capability"`
}

// ManifestDocs says where a manifest's documentation lives in git.
type ManifestDocs struct {
	Source *ManifestDocsSource `json:"source,omitempty"`
	// Index is the entry page, relative to Source.Path.
	Index string             `json:"index,omitempty"`
	Pages []ManifestDocsPage `json:"pages,omitempty"`
	Links []ManifestDocsLink `json:"links,omitempty"`
}

type ManifestDocsSource struct {
	Git  string `json:"git"`
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
}

type ManifestDocsPage struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	// Anchors maps a param name to a heading fragment on this page.
	Anchors map[string]string `json:"anchors,omitempty"`
}

type ManifestDocsLink struct {
	Rel   string `json:"rel"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}
