package devops

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// TestConnection verifies that the PAT can authenticate against the
// organization by listing its projects (the same check the operator uses
// to decide Connected vs Disconnected).
func (c *Client) TestConnection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.organizationURL+"/_apis/projects?api-version=7.0",
		nil,
	)
	if err != nil {
		return fmt.Errorf("azure devops: failed to build connection test request: %w", err)
	}

	resp, err := c.client.SendRequest(req)
	if err != nil {
		return fmt.Errorf("azure devops: connection test to %s failed: %w", c.organizationURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // best effort

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf(
			"azure devops: connection test to %s failed with status %d",
			c.organizationURL,
			resp.StatusCode,
		)
	}

	return nil
}

// ExtractOrganizationName derives a human readable organization name from
// an Azure DevOps URL, handling the different host formats:
//
//	Cloud:        https://dev.azure.com/organization
//	Cloud:        https://organization.visualstudio.com
//	On-premises:  https://tfs.company.com/tfs/CollectionName
//	On-premises:  https://azuredevops.company.com/CollectionName
func ExtractOrganizationName(azDoURL string) string {
	u, err := url.Parse(azDoURL)
	if err != nil || u.Host == "" {
		return "Invalid"
	}

	if u.Host == "dev.azure.com" {
		if seg := firstMeaningfulSegment(u.Path); seg != "" {
			return seg
		}
		return "Unknown"
	}

	if strings.HasSuffix(u.Host, ".visualstudio.com") {
		parts := strings.SplitN(u.Host, ".", 2)
		return parts[0]
	}

	// Self-hosted Azure DevOps Server / TFS:
	// /tfs/CollectionName -> CollectionName
	// /CollectionName     -> CollectionName
	segments := meaningfulSegments(u.Path)
	if len(segments) == 0 {
		return u.Host
	}
	if len(segments) > 1 && strings.EqualFold(segments[0], "tfs") {
		return segments[1]
	}
	return segments[0]
}

func firstMeaningfulSegment(path string) string {
	segments := meaningfulSegments(path)
	if len(segments) == 0 {
		return ""
	}
	return segments[0]
}

func meaningfulSegments(path string) []string {
	var segments []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	return segments
}
