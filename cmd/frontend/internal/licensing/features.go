package licensing

import (
	"strings"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend"
)

// Make the Site.productSubscription.fullProductName GraphQL field (and other places) use the proper
// product name.
func init() {
	graphqlbackend.GetFullProductName = fullProductName
}

const (
	// EnterpriseStarterTag is the license tag for Enterprise Starter (which includes only a subset
	// of Enterprise features).
	EnterpriseStarterTag = "starter"
)

var (
	// EnterpriseStarterTags is the license tags for Enterprise Starter.
	EnterpriseStarterTags = []string{EnterpriseStarterTag}

	// EnterpriseTags is the license tags for Enterprise (intentionally empty because it has no
	// feature restrictions)
	EnterpriseTags = []string{}
)

// fullProductName returns the full product name (e.g., "Sourcegraph Enterprise") based on the
// license info.
func fullProductName(hasLicense bool, licenseTags []string) string {
	if !hasLicense {
		return "Sourcegraph Core"
	}

	hasTag := func(tag string) bool {
		for _, t := range licenseTags {
			if tag == t {
				return true
			}
		}
		return false
	}

	var name string
	if hasTag("starter") {
		name = " Starter"
	}

	var misc []string
	if hasTag("trial") {
		misc = append(misc, "trial")
	}
	if hasTag("dev") {
		misc = append(misc, "dev use only")
	}
	if len(misc) > 0 {
		name += " (" + strings.Join(misc, ", ") + ")"
	}

	return "Sourcegraph Enterprise" + name
}