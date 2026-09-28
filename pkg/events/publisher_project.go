package events

import (
	"context"
	"strings"
)

type publisherProjectKey struct{}

// WithPublisherProject marks work done under ctx as acting for projectID, so
// system events it raises (sandbox lifecycle, agent completion) are attributed
// to that Project and delivered within its scope.
func WithPublisherProject(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, publisherProjectKey{}, strings.TrimSpace(projectID))
}

// PublisherProject returns the Project that work under ctx acts for, or empty
// when ctx carries none.
func PublisherProject(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	projectID, _ := ctx.Value(publisherProjectKey{}).(string)
	return projectID
}
