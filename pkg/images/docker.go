package images

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func DockerSummaryToProtoImage(image typesimage.Summary, inspectedAt, imageRef string) *agentcomposev2.Image {
	repoTags := CleanDockerRefs(image.RepoTags)
	repoDigests := CleanDockerRefs(image.RepoDigests)
	ref := FirstNonEmpty(strings.TrimSpace(imageRef), FirstString(repoTags), FirstString(repoDigests), strings.TrimSpace(image.ID))
	return &agentcomposev2.Image{
		ImageId:            image.ID,
		ImageRef:           ref,
		ResolvedRef:        FirstNonEmpty(FirstString(repoDigests), FirstString(repoTags), strings.TrimSpace(image.ID)),
		RepoTags:           repoTags,
		RepoDigests:        repoDigests,
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		SizeBytes:          NonNegativeUint64(image.Size),
		VirtualSizeBytes:   NonNegativeUint64(image.Size),
		CreatedAt:          UnixSecondsString(image.Created),
		InspectedAt:        inspectedAt,
		Dangling:           DockerImageDangling(repoTags, repoDigests),
		ContainerCount:     NonNegativeUint64(image.Containers),
		Docker: &agentcomposev2.DockerImageStatus{
			Local:           true,
			ParentId:        image.ParentID,
			SharedSizeBytes: image.SharedSize,
		},
		Labels: CloneStringMap(image.Labels),
	}
}

func DockerInspectToProtoImage(image typesimage.InspectResponse, inspectedAt, imageRef string) *agentcomposev2.Image {
	repoTags := CleanDockerRefs(image.RepoTags)
	repoDigests := CleanDockerRefs(image.RepoDigests)
	labels := map[string]string(nil)
	if image.Config != nil {
		labels = CloneStringMap(image.Config.Labels)
	}
	return &agentcomposev2.Image{
		ImageId:            image.ID,
		ImageRef:           FirstNonEmpty(strings.TrimSpace(imageRef), FirstString(repoTags), FirstString(repoDigests), image.ID),
		ResolvedRef:        FirstNonEmpty(FirstString(repoDigests), FirstString(repoTags), image.ID),
		RepoTags:           repoTags,
		RepoDigests:        repoDigests,
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           image.Os,
			Architecture: image.Architecture,
			Variant:      image.Variant,
			OsVersion:    image.OsVersion,
		},
		SizeBytes:        NonNegativeUint64(image.Size),
		VirtualSizeBytes: NonNegativeUint64(image.Size),
		CreatedAt:        image.Created,
		InspectedAt:      inspectedAt,
		Dangling:         DockerImageDangling(repoTags, repoDigests),
		Docker: &agentcomposev2.DockerImageStatus{
			Local:    true,
			ParentId: "",
		},
		Labels: labels,
	}
}

func ConsumeDockerImagePullProgress(reader io.Reader) ([]*agentcomposev2.ImagePullProgress, error) {
	decoder := json.NewDecoder(reader)
	var progress []*agentcomposev2.ImagePullProgress
	for {
		var payload struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			Progress    string `json:"progress"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Detail struct {
				Current uint64 `json:"current"`
				Total   uint64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := decoder.Decode(&payload); err != nil {
			if err == io.EOF {
				return progress, nil
			}
			return progress, err
		}
		if payload.Error != "" {
			return progress, errors.New(strings.TrimSpace(payload.Error))
		}
		if payload.ErrorDetail != nil && strings.TrimSpace(payload.ErrorDetail.Message) != "" {
			return progress, errors.New(strings.TrimSpace(payload.ErrorDetail.Message))
		}
		if payload.ID == "" && payload.Status == "" && payload.Progress == "" {
			continue
		}
		progress = append(progress, &agentcomposev2.ImagePullProgress{
			Id:           payload.ID,
			Status:       payload.Status,
			Progress:     payload.Progress,
			CurrentBytes: payload.Detail.Current,
			TotalBytes:   payload.Detail.Total,
		})
	}
}

func PaginateProtoImages(images []*agentcomposev2.Image, offset, limit uint32) ([]*agentcomposev2.Image, bool, uint32) {
	total := uint32(len(images))
	if offset > total {
		offset = total
	}
	if limit == 0 {
		limit = total - offset
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return images[offset:end], end < total, end
}

func CleanDockerRefs(refs []string) []string {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "<none>:<none>" || ref == "<none>@<none>" {
			continue
		}
		result = append(result, ref)
	}
	slices.Sort(result)
	return result
}

func DockerImageDangling(tags, digests []string) bool {
	return len(tags) == 0 && len(digests) == 0
}

func UnixSecondsString(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format(time.RFC3339Nano)
}

func DockerPlatformString(platform *agentcomposev2.ImagePlatform) string {
	if platform == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(platform.GetOs()), strings.TrimSpace(platform.GetArchitecture())}
	if parts[0] == "" || parts[1] == "" {
		return ""
	}
	if variant := strings.TrimSpace(platform.GetVariant()); variant != "" {
		parts = append(parts, variant)
	}
	return strings.Join(parts, "/")
}

func DockerEndpointFromEnv() string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return host
	}
	if host := strings.TrimSpace(client.DefaultDockerHost); host != "" {
		return host
	}
	return "docker daemon"
}
