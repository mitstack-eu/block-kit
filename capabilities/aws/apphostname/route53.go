package apphostname

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Route53 drives Route 53 through the AWS CLI with the operator's own credentials, since the apps zone lives in the platform's account.
type Route53 struct{}

func (Route53) LookupHost(ctx context.Context, zoneID, name string) (Held, error) {
	addresses, err := lookupRecord(ctx, zoneID, name, TypeA)
	if err != nil {
		return Held{}, err
	}
	owners, err := lookupRecord(ctx, zoneID, name, TypeTXT)
	if err != nil {
		return Held{}, err
	}
	// A TXT record holding several values reads as all of them, so it never matches one owner.
	return Held{Addresses: addresses, Owner: strings.Join(owners, " ")}, nil
}

func lookupRecord(ctx context.Context, zoneID, name, recordType string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "aws", "route53", "list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--start-record-name", name,
		"--start-record-type", recordType,
		"--max-items", "1",
		"--output", "json").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("route53 lookup of %s %s: %w: %s", recordType, name, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("route53 lookup of %s %s: %w", recordType, name, err)
	}
	return recordValues(out, name, recordType)
}

// recordValues reads name's record set of recordType from a list-resource-record-sets answer in its order, unquoting each TXT value, and nil when the set is absent.
func recordValues(listing []byte, name, recordType string) ([]string, error) {
	var listed struct {
		ResourceRecordSets []struct {
			Name            string
			Type            string
			ResourceRecords []struct{ Value string }
		}
	}
	if err := json.Unmarshal(listing, &listed); err != nil {
		return nil, fmt.Errorf("route53 lookup of %s %s: reading the answer: %w", recordType, name, err)
	}
	for _, set := range listed.ResourceRecordSets {
		if set.Type != recordType || !strings.EqualFold(strings.TrimSuffix(set.Name, "."), name) || len(set.ResourceRecords) == 0 {
			continue
		}
		values := make([]string, 0, len(set.ResourceRecords))
		for _, r := range set.ResourceRecords {
			values = append(values, unquoteTXT(recordType, r.Value))
		}
		return values, nil
	}
	return nil, nil
}

func unquoteTXT(recordType, value string) string {
	if recordType != TypeTXT {
		return value
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func (Route53) ChangeRecords(ctx context.Context, zoneID string, changes []Change) error {
	payload, err := changeBatchJSON(changes)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "aws", "route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", string(payload)).CombinedOutput()
	if err == nil {
		return nil
	}
	output := strings.TrimSpace(string(out))
	if isRecordConflict(output) {
		return fmt.Errorf("route53 change of %s: %s: %w", changes[0].Name, output, ErrConflict)
	}
	return fmt.Errorf("route53 change of %s: %w: %s", changes[0].Name, err, output)
}

// changeBatchJSON quotes each TXT value, as Route 53 requires.
func changeBatchJSON(changes []Change) ([]byte, error) {
	encoded := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		records := make([]map[string]string, 0, len(c.Values))
		for _, value := range c.Values {
			if c.Type == TypeTXT {
				value = strconv.Quote(value)
			}
			records = append(records, map[string]string{"Value": value})
		}
		encoded = append(encoded, map[string]any{
			"Action": c.Action,
			"ResourceRecordSet": map[string]any{
				"Name":            c.Name,
				"Type":            c.Type,
				"TTL":             c.TTL,
				"ResourceRecords": records,
			},
		})
	}
	payload, err := json.Marshal(map[string]any{"Changes": encoded})
	if err != nil {
		return nil, fmt.Errorf("encoding the change batch: %w", err)
	}
	return payload, nil
}

// isRecordConflict reports whether the AWS CLI's output is Route 53 rejecting a batch over the records it found.
func isRecordConflict(output string) bool {
	if !strings.Contains(output, "InvalidChangeBatch") {
		return false
	}
	for _, found := range []string{"but it already exists", "but it was not found", "values provided do not match"} {
		if strings.Contains(output, found) {
			return true
		}
	}
	return false
}
