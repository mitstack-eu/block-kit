package continuity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mitstack-eu/block-kit/helpers/lease"
)

// LinkUpload and LinkDownload are the two uses a hook asks a snapshot link for.
const (
	LinkUpload   = "upload"
	LinkDownload = "download"
)

// LinkWant is the want a hook sends for a snapshot link's lease and the store's provider reads, answering a Link as the credential: an upload link for a new object of Unit, or a download link for the object Ref names.
type LinkWant struct {
	Use  string `json:"use"`
	Unit string `json:"unit"`
	Ref  string `json:"ref,omitempty"`
}

// ParseLinkWant reads the want of a lease request for a provider's Leaser, refusing one no hook sends.
func ParseLinkWant(raw json.RawMessage) (LinkWant, error) {
	var want LinkWant
	if err := json.Unmarshal(raw, &want); err != nil {
		return LinkWant{}, errors.New("continuity: the want is not one a hook asks a snapshot link with")
	}
	switch {
	case want.Unit == "":
		return LinkWant{}, errors.New("continuity: the want names no unit")
	case want.Use == LinkUpload && want.Ref == "":
		return want, nil
	case want.Use == LinkDownload && want.Ref != "":
		return want, nil
	}
	return LinkWant{}, fmt.Errorf("continuity: the want of unit %q asks for neither an upload of a new object nor a download of a named one", want.Unit)
}

// LeasedLinks is the Links of a program on a node: it asks the platform's lease route for each link with the node's report token, so the node holds no storage key, and Resource is the reference the block's manifest uses for the snapshot store.
type LeasedLinks struct {
	Platform lease.Platform
	Resource string
}

func (l LeasedLinks) Upload(ctx context.Context, unit string) (Link, error) {
	link, err := l.ask(ctx, LinkWant{Use: LinkUpload, Unit: unit})
	if err != nil {
		return Link{}, err
	}
	if link.Ref == "" {
		return Link{}, fmt.Errorf("the upload link leased from %q names no object", l.Resource)
	}
	return link, nil
}

func (l LeasedLinks) Download(ctx context.Context, unit, ref string) (Link, error) {
	return l.ask(ctx, LinkWant{Use: LinkDownload, Unit: unit, Ref: ref})
}

// ask never repeats the credential in an error, since a link carries its signature.
func (l LeasedLinks) ask(ctx context.Context, want LinkWant) (Link, error) {
	answer, err := l.Platform.Ask(ctx, l.Resource, want)
	if err != nil {
		return Link{}, err
	}
	var link Link
	if err := json.Unmarshal(answer.Credential, &link); err != nil || link.URL == "" {
		return Link{}, fmt.Errorf("the credential leased from %q holds no link", l.Resource)
	}
	return link, nil
}
