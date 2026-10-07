package observe

import (
	"context"
	"path"
	"strings"

	"github.com/mitstack-eu/operator-kit/contract"
)

// AWSSubscriptionTag is the tag key an AWS resource carries its
// subscription's id under.
const AWSSubscriptionTag = "mitstack:subscription-id"

// HetznerSubscriptionLabel is the label key a Hetzner object carries its
// subscription's id under.
const HetznerSubscriptionLabel = "mitstack.dev/subscription"

// Tagged is one object found carrying a subscription's id, as the type it
// is. Type is empty for an object the lister cannot type, whose ID then
// names it in full.
type Tagged struct {
	Type string
	ID   string
}

// TaggedLister lists everything carrying a subscription's id.
type TaggedLister interface {
	ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error)
}

// AWSTaggingAPI lists the ARNs of the resources carrying a tag.
type AWSTaggingAPI interface {
	ResourceARNs(ctx context.Context, d contract.Desired, tagKey, tagValue string) ([]string, error)
}

// AWSTagged lists the AWS resources tagged with a subscription's id under
// AWSSubscriptionTag.
type AWSTagged struct {
	API AWSTaggingAPI
}

// awsResourceTypes maps an ARN's service and resource type to a type id.
var awsResourceTypes = map[string]string{
	"ec2/instance":       "aws.ec2-instance",
	"ec2/vpc":            "aws.vpc",
	"ec2/security-group": "aws.security-group",
	"ec2/volume":         "aws.ebs-volume",
	"iam/role":           "aws.iam-role",
	"ecr/repository":     "aws.ecr-repository",
}

// ListTagged implements TaggedLister.
func (l AWSTagged) ListTagged(ctx context.Context, d contract.Desired, subscriptionID string) ([]Tagged, error) {
	arns, err := l.API.ResourceARNs(ctx, d, AWSSubscriptionTag, subscriptionID)
	if err != nil {
		return nil, err
	}
	out := make([]Tagged, 0, len(arns))
	for _, arn := range arns {
		out = append(out, taggedARN(arn))
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
	if typ == "aws.iam-role" {
		id = path.Base(id)
	}
	return Tagged{Type: typ, ID: id}
}

// HetznerObject is one object the Hetzner Cloud API lists, by its kind, as
// the API names it, and its id.
type HetznerObject struct {
	Kind string
	ID   string
}

// HetznerLabelAPI lists the objects matching a label selector.
type HetznerLabelAPI interface {
	ListByLabel(ctx context.Context, d contract.Desired, selector string) ([]HetznerObject, error)
}

// HetznerLabelled lists the Hetzner objects labelled with a subscription's
// id under HetznerSubscriptionLabel.
type HetznerLabelled struct {
	API HetznerLabelAPI
}

// hetznerTypes maps a Hetzner object kind to a type id.
var hetznerTypes = map[string]string{
	"server":     "hetzner.server",
	"primary_ip": "hetzner.primary-ip",
	"firewall":   "hetzner.firewall",
	"volume":     "hetzner.volume",
	"ssh_key":    "hetzner.ssh-key",
}

// ListTagged implements TaggedLister.
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
