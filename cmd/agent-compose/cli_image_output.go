package main

import (
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

func writeImagePullText(out io.Writer, output composeImagePullOutput) error {
	status := "Pulled"
	if imagePullSkipped(output) {
		status = "Skipped"
	}
	if _, err := fmt.Fprintf(out, "%s %s\nResolved: %s\n", status, output.ImageRef, firstNonEmptyString(output.ResolvedRef, "-")); err != nil {
		return err
	}
	for _, warning := range output.Warnings {
		if _, err := fmt.Fprintf(out, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

type composeImageListOutput struct {
	Images      []composeImageOutput    `json:"images"`
	TotalCount  uint32                  `json:"total_count"`
	HasMore     bool                    `json:"has_more"`
	NextOffset  uint32                  `json:"next_offset"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImageInspectOutput struct {
	Image       composeImageOutput      `json:"image"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImagePullOutput struct {
	ImageRef    string                     `json:"image_ref"`
	ResolvedRef string                     `json:"resolved_ref,omitempty"`
	Status      string                     `json:"status"`
	Image       composeImageOutput         `json:"image"`
	Progress    []composeImageProgressItem `json:"progress,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
}

type composeProjectImagePullOutput struct {
	Images []composeImagePullOutput `json:"images"`
}

type composeImageBuildOutput struct {
	ImageRef    string             `json:"image_ref"`
	ResolvedRef string             `json:"resolved_ref,omitempty"`
	Status      string             `json:"status"`
	Image       composeImageOutput `json:"image"`
	Warnings    []string           `json:"warnings,omitempty"`
}

type composeProjectImageBuildOutput struct {
	Images []composeImageBuildOutput `json:"images"`
}

type composeImageRemoveOutput struct {
	ImageRef     string   `json:"image_ref"`
	UntaggedRefs []string `json:"untagged_refs,omitempty"`
	DeletedIDs   []string `json:"deleted_ids,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

type composeImageOutput struct {
	ImageID            string            `json:"image_id"`
	ShortID            string            `json:"short_id,omitempty"`
	ImageRef           string            `json:"image_ref"`
	ResolvedRef        string            `json:"resolved_ref,omitempty"`
	RepoTags           []string          `json:"repo_tags,omitempty"`
	RepoDigests        []string          `json:"repo_digests,omitempty"`
	Store              string            `json:"store"`
	AvailabilityStatus string            `json:"availability_status"`
	Platform           string            `json:"platform,omitempty"`
	SizeBytes          uint64            `json:"size_bytes"`
	VirtualSizeBytes   uint64            `json:"virtual_size_bytes"`
	CreatedAt          string            `json:"created_at,omitempty"`
	InspectedAt        string            `json:"inspected_at,omitempty"`
	Dangling           bool              `json:"dangling"`
	ContainerCount     uint64            `json:"container_count"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type composeImageStoreOutput struct {
	Store     string `json:"store"`
	Available bool   `json:"available"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

func composeImageListOutputFromResponse(resp *agentcomposev2.ListImagesResponse) composeImageListOutput {
	output := composeImageListOutput{
		Images:      make([]composeImageOutput, 0, len(resp.GetImages())),
		TotalCount:  resp.GetTotalCount(),
		HasMore:     resp.GetHasMore(),
		NextOffset:  resp.GetNextOffset(),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
	for _, image := range resp.GetImages() {
		output.Images = append(output.Images, composeImageOutputFromProto(image))
	}
	return output
}

func composeImagePullOutputFromResponse(resp *agentcomposev2.PullImageResponse) composeImagePullOutput {
	output := composeImagePullOutput{
		ImageRef:    firstNonEmptyString(resp.GetImage().GetImageRef(), resp.GetResolvedRef()),
		ResolvedRef: resp.GetResolvedRef(),
		Status:      imageOperationStatusText(resp.GetStatus()),
		Image:       composeImageOutputFromProto(resp.GetImage()),
		Warnings:    append([]string(nil), resp.GetWarnings()...),
		Progress:    make([]composeImageProgressItem, 0, len(resp.GetProgress())),
	}
	for _, item := range resp.GetProgress() {
		output.Progress = append(output.Progress, composeImageProgressItem{
			ID:           displayOpaqueID(item.GetId()),
			Status:       item.GetStatus(),
			Progress:     item.GetProgress(),
			CurrentBytes: item.GetCurrentBytes(),
			TotalBytes:   item.GetTotalBytes(),
		})
	}
	return output
}

func composeImageInspectOutputFromResponse(resp *agentcomposev2.InspectImageResponse) composeImageInspectOutput {
	return composeImageInspectOutput{
		Image:       composeImageOutputFromProto(resp.GetImage()),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
}

func composeImageRemoveOutputFromResponse(resp *agentcomposev2.RemoveImageResponse) composeImageRemoveOutput {
	return composeImageRemoveOutput{
		ImageRef:     resp.GetImageRef(),
		UntaggedRefs: append([]string(nil), resp.GetUntaggedRefs()...),
		DeletedIDs:   displayOpaqueIDs(resp.GetDeletedIds()),
		Warnings:     append([]string(nil), resp.GetWarnings()...),
	}
}

func composeImageOutputFromProto(image *agentcomposev2.Image) composeImageOutput {
	if image == nil {
		return composeImageOutput{}
	}
	return composeImageOutput{
		ImageID:            displayOpaqueID(image.GetImageId()),
		ShortID:            identity.ShortID(image.GetImageId()),
		ImageRef:           image.GetImageRef(),
		ResolvedRef:        image.GetResolvedRef(),
		RepoTags:           append([]string(nil), image.GetRepoTags()...),
		RepoDigests:        append([]string(nil), image.GetRepoDigests()...),
		Store:              imageStoreText(image.GetStore()),
		AvailabilityStatus: imageAvailabilityStatusText(image.GetAvailabilityStatus()),
		Platform:           imagePlatformText(image.GetPlatform()),
		SizeBytes:          image.GetSizeBytes(),
		VirtualSizeBytes:   image.GetVirtualSizeBytes(),
		CreatedAt:          image.GetCreatedAt(),
		InspectedAt:        image.GetInspectedAt(),
		Dangling:           image.GetDangling(),
		ContainerCount:     image.GetContainerCount(),
		Labels:             cloneStringMapForCLI(image.GetLabels()),
	}
}

func composeImageStoreOutputFromProto(status *agentcomposev2.ImageStoreStatus) composeImageStoreOutput {
	if status == nil {
		return composeImageStoreOutput{}
	}
	return composeImageStoreOutput{
		Store:     imageStoreText(status.GetStore()),
		Available: status.GetAvailable(),
		Endpoint:  status.GetEndpoint(),
		Error:     status.GetError(),
	}
}

func writeImagesText(out io.Writer, images []composeImageOutput, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "IMAGE ID\tREF\tDISK USAGE"
	if verbose {
		header = "REF\tIMAGE ID\tSTORE\tSTATUS\tPLATFORM\tDISK USAGE\tCONTENT SIZE\tCREATED"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for _, image := range images {
		ref := imageListRefForText(image)
		diskUsage := formatImageSizeForText(firstNonZeroUint64(image.VirtualSizeBytes, image.SizeBytes))
		if verbose {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				ref,
				shortImageID(image.ImageID),
				firstNonEmptyString(image.Store, "-"),
				firstNonEmptyString(image.AvailabilityStatus, "-"),
				firstNonEmptyString(image.Platform, "-"),
				diskUsage,
				formatImageSizeForText(image.SizeBytes),
				formatImageCreatedForText(image.CreatedAt),
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n",
			shortImageID(image.ImageID),
			ref,
			diskUsage,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func imageListRefForText(image composeImageOutput) string {
	if ref := firstNonEmptyString(image.RepoTags...); ref != "" {
		return ref
	}
	ref := firstNonEmptyString(image.ImageRef, image.ResolvedRef)
	if imageRefLooksUntagged(ref, image.ImageID) {
		return "<none>"
	}
	return firstNonEmptyString(ref, "<none>")
}

func formatImageSizeForText(size uint64) string {
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	div := uint64(unit)
	exp := 0
	for n := size / unit; n >= unit && exp < len("KMGTPE")-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatImageCreatedForText(createdAt string) string {
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		return "-"
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return createdAt
	}
	now := time.Now().UTC()
	if created.After(now) {
		return "in " + formatImageAgeForText(created.Sub(now))
	}
	return formatImageAgeForText(now.Sub(created)) + " ago"
}

func formatImageAgeForText(age time.Duration) string {
	if age < time.Minute {
		return "less than a minute"
	}
	if age < time.Hour {
		return pluralizeImageAge(int(age/time.Minute), "minute")
	}
	if age < 24*time.Hour {
		return pluralizeImageAge(int(age/time.Hour), "hour")
	}
	if age < 30*24*time.Hour {
		return pluralizeImageAge(int(age/(24*time.Hour)), "day")
	}
	if age < 365*24*time.Hour {
		return pluralizeImageAge(int(age/(30*24*time.Hour)), "month")
	}
	return pluralizeImageAge(int(age/(365*24*time.Hour)), "year")
}

func parseImagePlatform(value string) (*agentcomposev2.ImagePlatform, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("invalid --platform %q: expected os/arch[/variant]", value)
	}
	platform := &agentcomposev2.ImagePlatform{
		Os:           strings.TrimSpace(parts[0]),
		Architecture: strings.TrimSpace(parts[1]),
	}
	if len(parts) == 3 {
		platform.Variant = strings.TrimSpace(parts[2])
	}
	return platform, nil
}

func imageStoreText(store agentcomposev2.ImageStoreKind) string {
	switch store {
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON:
		return "docker"
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE:
		return "oci-cache"
	default:
		return "unspecified"
	}
}

func imageAvailabilityStatusText(status agentcomposev2.ImageAvailabilityStatus) string {
	switch status {
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE:
		return "available"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING:
		return "missing"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR:
		return "error"
	default:
		return "unspecified"
	}
}

func imageOperationStatusText(status agentcomposev2.ImageOperationStatus) string {
	switch status {
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED:
		return "failed"
	default:
		return "unspecified"
	}
}

func imagePlatformText(platform *agentcomposev2.ImagePlatform) string {
	if platform == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(platform.GetOs()), strings.TrimSpace(platform.GetArchitecture())}
	if strings.TrimSpace(platform.GetVariant()) != "" {
		parts = append(parts, strings.TrimSpace(platform.GetVariant()))
	}
	if parts[0] == "" || parts[1] == "" {
		return strings.Trim(strings.Join(parts, "/"), "/")
	}
	return strings.Join(parts, "/")
}

func shortImageID(id string) string {
	id = displayOpaqueID(id)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
