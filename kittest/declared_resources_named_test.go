package kittest_test

// declared_resources_named_test.go pins CheckDeclaredResources driving one
// declared resource the way the engine drives it, by name: with
// DeclaredResources.Resource set, only that resource is ensured twice,
// observed, deleted and proved absent, under a Desired naming it, and a
// name the manifest does not declare is one problem with nothing driven. A
// driven resource that reports its ensure is checked through
// EnsureWithReport, and a second ensure reporting a different identity from
// the first is a problem naming the resource.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// reportingScripted is a scriptedResource that also reports what its
// ensure stored. Its EnsureWithReport writes its own entry to the shared
// log, so an "ensure" entry for it proves the plain Ensure ran too.
type reportingScripted struct {
	*scriptedResource
	identity func(ensures int) map[string]string
}

func (r *reportingScripted) EnsureWithReport(ctx context.Context, d contract.Desired) (contract.Ensured, error) {
	r.record("ensure with report", d.SubscriptionID)
	if err := r.FakeResource.Ensure(ctx, d); err != nil {
		return contract.Ensured{}, err
	}
	return contract.Ensured{Identity: r.identity(r.EnsureCalls)}, nil
}

var _ handler.ReportingResource = (*reportingScripted)(nil)

// declaredOnly drops the supplied resource's entries from log, leaving the
// calls made on declared resources.
func declaredOnly(log []string) []string {
	var out []string
	for _, entry := range log {
		if !strings.HasPrefix(entry, "supplied ") {
			out = append(out, entry)
		}
	}
	return out
}

func untouched(r *scriptedResource) bool {
	return r.EnsureCalls+r.ObserveCalls+r.DeleteCalls == 0
}

func TestCheckDeclaredResources_DrivesOnlyTheNamedResource(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Resource = "instance"

	if problems := kittest.CheckDeclaredResources(in); len(problems) != 0 {
		t.Errorf("a converging named resource must have no problems, got %q", problems)
	}
	want := []string{
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance ensure sub-checked",
		"instance observe sub-checked",
		"instance delete sub-checked",
		"instance observe sub-checked",
	}
	if got := declaredOnly(*b.log); !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !untouched(b.network) {
		t.Errorf("network saw ensure=%d observe=%d delete=%d, want no call: the check names instance",
			b.network.EnsureCalls, b.network.ObserveCalls, b.network.DeleteCalls)
	}
}

func TestCheckDeclaredResources_HandsTheNamedResourceADesiredNamingIt(t *testing.T) {
	b := newCheckedBlock()
	var got contract.Desired
	in := b.input()
	in.Resources["instance"] = &desiredCapture{DeclaredResource: b.instance, got: &got}
	in.Resource = "instance"

	kittest.CheckDeclaredResources(in)
	want := checkedDesired
	want.Resource = "instance"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ensure received %+v, want the Desired the check was given, naming the resource: %+v", got, want)
	}
}

func TestCheckDeclaredResources_ProblemNamingAResourceTheManifestDoesNotDeclare(t *testing.T) {
	b := newCheckedBlock()
	in := b.input()
	in.Resource = "cache"

	problems := kittest.CheckDeclaredResources(in)
	if len(problems) != 1 {
		t.Fatalf("problems = %q, want exactly one naming the undeclared resource %q", problems, "cache")
	}
	if !strings.Contains(problems[0], `"cache"`) {
		t.Errorf("problem %q does not name the undeclared resource %q", problems[0], "cache")
	}
	if !untouched(b.network) || !untouched(b.instance) {
		t.Error("the check drove a declared resource, but the name it was given is not declared")
	}
}

// withReportingInstance swaps the block's instance for one reporting the
// identity identity gives for each ensure.
func withReportingInstance(b *checkedBlock, in kittest.DeclaredResources, identity func(ensures int) map[string]string) kittest.DeclaredResources {
	in.Resources["instance"] = &reportingScripted{scriptedResource: b.instance, identity: identity}
	return in
}

func stableIdentity(int) map[string]string { return map[string]string{"hostId": "i-0abc"} }

func changingIdentity(ensures int) map[string]string {
	if ensures > 1 {
		return map[string]string{"hostId": "i-0def"}
	}
	return map[string]string{"hostId": "i-0abc"}
}

func TestCheckDeclaredResources_EnsuresAReportingResourceThroughEnsureWithReport(t *testing.T) {
	for _, named := range []bool{true, false} {
		label := "every resource"
		if named {
			label = "the named resource"
		}
		t.Run(label, func(t *testing.T) {
			b := newCheckedBlock()
			in := withReportingInstance(b, b.input(), stableIdentity)
			if named {
				in.Resource = "instance"
			}

			if problems := kittest.CheckDeclaredResources(in); len(problems) != 0 {
				t.Errorf("a reporting resource whose identity holds must have no problems, got %q", problems)
			}
			reports := 0
			for _, entry := range *b.log {
				switch entry {
				case "instance ensure with report sub-checked":
					reports++
				case "instance ensure sub-checked":
					t.Errorf("the check called instance's plain Ensure, want EnsureWithReport in its place")
				}
			}
			if reports != 2 {
				t.Errorf("instance's EnsureWithReport ran %d times, want 2", reports)
			}
		})
	}
}

func TestCheckDeclaredResources_ProblemNamingAResourceWhoseSecondEnsureReportsADifferentIdentity(t *testing.T) {
	for _, named := range []bool{true, false} {
		label := "every resource"
		if named {
			label = "the named resource"
		}
		t.Run(label, func(t *testing.T) {
			b := newCheckedBlock()
			in := withReportingInstance(b, b.input(), changingIdentity)
			if named {
				in.Resource = "instance"
			}

			problems := kittest.CheckDeclaredResources(in)
			wantProblemsAllNaming(t, problems, `resource "instance": `)
			if !strings.Contains(strings.Join(problems, "\n"), "identity") {
				t.Errorf("problems %q do not say the identity changed", problems)
			}
		})
	}
}
