package github

import (
	"context"
	"errors"
	"net/http"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Compare answers whether base is included in head within repo
// ("owner/name"): Included when head is ahead of or identical to base,
// NotIncluded when it is behind or has diverged. A 404 (an unknown or
// force-pushed-away commit) is InclusionUnknown with no error; other
// failures return the error.
func (c *Client) Compare(ctx context.Context, repo, base, head string) (model.Inclusion, error) {
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
