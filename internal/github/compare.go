package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// shaRE is a full or abbreviated commit SHA.
var shaRE = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// Compare answers whether base is included in head within repo
// ("owner/name"): Included when head is ahead of or identical to base,
// NotIncluded when it is behind or has diverged. A 404 (an unknown or
// force-pushed-away commit) is InclusionUnknown with no error; other
// failures return the error, as does a base or head that is not a 7 to 40
// character hex SHA (checked before any request).
func (c *Client) Compare(ctx context.Context, repo, base, head string) (model.Inclusion, error) {
	if !shaRE.MatchString(base) || !shaRE.MatchString(head) {
		return model.InclusionUnknown, fmt.Errorf("github: compare %s: %q...%q are not commit SHAs", repo, base, head)
	}
	var out struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/compare/"+base+"..."+head+"?per_page=1", nil, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return model.InclusionUnknown, nil
	}
	if err != nil {
		return model.InclusionUnknown, err
	}
	switch out.Status {
	case "ahead", "identical":
		return model.Included, nil
	case "behind", "diverged":
		return model.NotIncluded, nil
	}
	return model.InclusionUnknown, nil
}
