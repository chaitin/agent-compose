package main

import (
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeImageListOptions struct {
	Query   string
	All     bool
	Verbose bool
}

type composeImagePullOptions struct {
	Platform string
}

type composeImageBuildOptions struct {
	Tags       []string
	Dockerfile string
	Target     string
	BuildArgs  []string
	Platform   string
	NoCache    bool
	Pull       bool
}

type composeImageRemoveOptions struct {
	Force         bool
	PruneChildren bool
}

func addImageListFlags(cmd *cobra.Command, options *composeImageListOptions) {
	cmd.Flags().StringVar(&options.Query, "query", "", "Filter images by reference")
	cmd.Flags().BoolVarP(&options.All, "all", "a", false, "Show all images")
	cmd.Flags().BoolVar(&options.Verbose, "verbose", false, "Show all image details")
}

func addImagePullFlags(cmd *cobra.Command, options *composeImagePullOptions) {
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Pull platform as os/arch[/variant]")
}

func addImageBuildFlags(cmd *cobra.Command, options *composeImageBuildOptions) {
	cmd.Flags().StringArrayVarP(&options.Tags, "tag", "t", nil, "Name and optionally tag in name:tag format")
	cmd.Flags().StringVar(&options.Dockerfile, "dockerfile", "", "Name of the Dockerfile")
	cmd.Flags().StringVar(&options.Target, "target", "", "Build target stage")
	cmd.Flags().StringArrayVar(&options.BuildArgs, "build-arg", nil, "Set build-time variables")
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Build platform as os/arch[/variant]")
	cmd.Flags().BoolVar(&options.NoCache, "no-cache", false, "Do not use cache when building")
	cmd.Flags().BoolVar(&options.Pull, "pull", false, "Always attempt to pull a newer base image")
}

func addImageRemoveFlags(cmd *cobra.Command, options *composeImageRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Force image removal")
	cmd.Flags().BoolVar(&options.PruneChildren, "prune-children", false, "Remove untagged child images")
}

func runComposeImageListCommand(cmd *cobra.Command, cli cliOptions, options composeImageListOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.ListImages(cmd.Context(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Query: strings.TrimSpace(options.Query),
		All:   options.All,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list images: %w", err))
	}
	output := composeImageListOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeImagesText(cmd.OutOrStdout(), output.Images, options.Verbose)
}

func runComposePullCommand(cmd *cobra.Command, cli cliOptions, options composeImagePullOptions, args []string) error {
	if len(args) == 1 {
		return runComposeImagePullCommand(cmd, cli, options, args[0])
	}
	_, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	imageRefs := projectImageRefs(normalized)
	if len(imageRefs) == 0 {
		if cli.JSON {
			data, err := json.MarshalIndent(composeProjectImagePullOutput{Images: []composeImagePullOutput{}}, "", "  ")
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No project images configured")
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	platform, err := parseImagePlatform(options.Platform)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	output := composeProjectImagePullOutput{
		Images: make([]composeImagePullOutput, 0, len(imageRefs)),
	}
	for _, imageRef := range imageRefs {
		item, err := pullImage(cmd.Context(), clients.image, imageRef, platform)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("pull image %s: %w", imageRef, err))
		}
		output.Images = append(output.Images, item)
		if !cli.JSON {
			if err := writeImagePullText(cmd.OutOrStdout(), item); err != nil {
				return err
			}
		}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return nil
}

func projectImageRefs(project *compose.NormalizedProjectSpec) []string {
	seen := make(map[string]struct{}, len(project.Agents))
	refs := make([]string, 0, len(project.Agents))
	for _, agent := range project.Agents {
		imageRef := strings.TrimSpace(agent.Image)
		if imageRef == "" {
			continue
		}
		if _, ok := seen[imageRef]; ok {
			continue
		}
		seen[imageRef] = struct{}{}
		refs = append(refs, imageRef)
	}
	return refs
}

func runComposeImagePullCommand(cmd *cobra.Command, cli cliOptions, options composeImagePullOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	platform, err := parseImagePlatform(options.Platform)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	output, err := pullImage(cmd.Context(), clients.image, strings.TrimSpace(imageRef), platform)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("pull image %s: %w", strings.TrimSpace(imageRef), err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeImagePullText(cmd.OutOrStdout(), output)
}

func runComposeBuildCommand(cmd *cobra.Command, cli cliOptions, options composeImageBuildOptions, args []string) error {
	return runComposeProjectBuildCommand(cmd, cli, options, args)
}

func runComposeProjectBuildCommand(cmd *cobra.Command, cli cliOptions, options composeImageBuildOptions, agentNames []string) error {
	sourcePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	plans, err := projectImageBuildPlans(sourcePath, normalized, options, agentNames)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	if len(plans) == 0 {
		if cli.JSON {
			data, err := json.MarshalIndent(composeProjectImageBuildOutput{Images: []composeImageBuildOutput{}}, "", "  ")
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No project images configured for build")
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeProjectImageBuildOutput{Images: make([]composeImageBuildOutput, 0, len(plans))}
	for _, plan := range plans {
		item, err := buildImage(cmd.Context(), cmd.OutOrStdout(), cli.JSON, clients.image, plan)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("build image %s: %w", firstNonEmptyString(plan.GetTags()...), err))
		}
		output.Images = append(output.Images, item)
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return nil
}

func buildImage(ctx context.Context, out io.Writer, jsonOutput bool, client agentcomposev2connect.ImageServiceClient, req *agentcomposev2.BuildImageRequest) (composeImageBuildOutput, error) {
	stream, err := client.BuildImage(ctx, connect.NewRequest(req))
	if err != nil {
		return composeImageBuildOutput{}, err
	}
	output := composeImageBuildOutput{
		ImageRef: firstNonEmptyString(req.GetTags()...),
		Status:   imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING),
	}
	for stream.Receive() {
		event := stream.Msg()
		if !jsonOutput && strings.TrimSpace(event.GetMessage()) != "" {
			if _, err := fmt.Fprintln(out, strings.TrimSpace(event.GetMessage())); err != nil {
				return output, err
			}
		}
		if event.GetImage() != nil {
			output.Image = composeImageOutputFromProto(event.GetImage())
		}
		if strings.TrimSpace(event.GetImageRef()) != "" {
			output.ImageRef = event.GetImageRef()
		}
		if strings.TrimSpace(event.GetResolvedRef()) != "" {
			output.ResolvedRef = event.GetResolvedRef()
		}
		if event.GetStatus() != agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_UNSPECIFIED {
			output.Status = imageOperationStatusText(event.GetStatus())
		}
		output.Warnings = appendUniqueStrings(output.Warnings, event.GetWarnings()...)
	}
	if err := stream.Err(); err != nil {
		return output, err
	}
	return output, nil
}

func projectImageBuildPlans(sourcePath string, project *compose.NormalizedProjectSpec, options composeImageBuildOptions, agentNames []string) ([]*agentcomposev2.BuildImageRequest, error) {
	selected := map[string]struct{}{}
	for _, name := range agentNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		selected[name] = struct{}{}
	}
	composeDir := "."
	if strings.TrimSpace(sourcePath) != "" {
		composeDir = filepath.Dir(sourcePath)
	}
	var plans []*agentcomposev2.BuildImageRequest
	for _, agent := range project.Agents {
		if len(selected) > 0 {
			if _, ok := selected[agent.Name]; !ok {
				continue
			}
			delete(selected, agent.Name)
		}
		if agent.Build == nil {
			continue
		}
		req, err := buildImageRequestFromAgent(composeDir, agent, options)
		if err != nil {
			return nil, err
		}
		plans = append(plans, req)
	}
	if len(selected) > 0 {
		missing := make([]string, 0, len(selected))
		for name := range selected {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown build agent(s): %s", strings.Join(missing, ", "))
	}
	return plans, nil
}

func buildImageRequestFromAgent(composeDir string, agent compose.NormalizedAgentSpec, options composeImageBuildOptions) (*agentcomposev2.BuildImageRequest, error) {
	build := agent.Build
	tags := append([]string{}, agent.Image)
	tags = append(tags, build.Tags...)
	tags = append(tags, options.Tags...)
	contextDir := resolveComposeBuildPath(composeDir, build.Context)
	dockerfile := build.Dockerfile
	if strings.TrimSpace(options.Dockerfile) != "" {
		dockerfile = options.Dockerfile
	}
	buildArgs := cloneStringMapForCLI(build.Args)
	cliArgs, err := parseBuildArgs(options.BuildArgs)
	if err != nil {
		return nil, err
	}
	for key, value := range cliArgs {
		if buildArgs == nil {
			buildArgs = map[string]string{}
		}
		buildArgs[key] = value
	}
	platformValue := ""
	if len(build.Platforms) == 1 {
		platformValue = build.Platforms[0]
	}
	if strings.TrimSpace(options.Platform) != "" {
		platformValue = options.Platform
	}
	platform, err := parseImagePlatform(platformValue)
	if err != nil {
		return nil, err
	}
	tags = normalizeCLIStringList(tags)
	if len(tags) == 0 {
		return nil, fmt.Errorf("agent %s build requires image or build.tags", agent.Name)
	}
	return &agentcomposev2.BuildImageRequest{
		ContextDir: contextDir,
		Dockerfile: strings.TrimSpace(dockerfile),
		Tags:       tags,
		BuildArgs:  buildArgs,
		Target:     firstNonEmptyString(options.Target, build.Target),
		Store:      agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		Platform:   platform,
		NoCache:    options.NoCache || build.NoCache,
		Pull:       options.Pull || build.Pull,
	}, nil
}

func pullImage(ctx context.Context, client agentcomposev2connect.ImageServiceClient, imageRef string, platform *agentcomposev2.ImagePlatform) (composeImagePullOutput, error) {
	resp, err := client.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{
		ImageRef: imageRef,
		Platform: platform,
	}))
	if err != nil {
		return composeImagePullOutput{}, err
	}
	return composeImagePullOutputFromResponse(resp.Msg), nil
}

func imagePullSkipped(output composeImagePullOutput) bool {
	for _, warning := range output.Warnings {
		normalized := strings.ToLower(strings.TrimSpace(warning))
		if strings.Contains(normalized, "skipped") || strings.Contains(normalized, "already exists") {
			return true
		}
	}
	return false
}

func runComposeImageRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeImageRemoveOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.RemoveImage(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveImageRequest{
		ImageRef:      strings.TrimSpace(imageRef),
		Force:         options.Force,
		PruneChildren: options.PruneChildren,
	}))
	if err != nil {
		return commandExitErrorForImageTarget("remove image", strings.TrimSpace(imageRef), err)
	}
	output := composeImageRemoveOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, ref := range output.UntaggedRefs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Untagged: %s\n", ref); err != nil {
			return err
		}
	}
	for _, id := range output.DeletedIDs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted: %s\n", id); err != nil {
			return err
		}
	}
	if len(output.UntaggedRefs) == 0 && len(output.DeletedIDs) == 0 {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed: %s\n", output.ImageRef)
		return err
	}
	return nil
}

func commandExitErrorForImageTarget(operation, imageRef string, err error) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return commandExitError{
			Code: exitCodeUsage,
			Err:  fmt.Errorf("image %s does not exist", imageRef),
		}
	}
	return commandExitErrorForConnect(fmt.Errorf("%s %s: %w", operation, imageRef, err))
}

func runComposeImageInspectCommand(cmd *cobra.Command, cli cliOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.InspectImage(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectImageRequest{
		ImageRef: strings.TrimSpace(imageRef),
	}))
	if err != nil {
		return commandExitErrorForImageTarget("inspect image", strings.TrimSpace(imageRef), err)
	}
	output := composeImageInspectOutputFromResponse(resp.Msg)
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}

type composeImageProgressItem struct {
	ID           string `json:"id,omitempty"`
	Status       string `json:"status,omitempty"`
	Progress     string `json:"progress,omitempty"`
	CurrentBytes uint64 `json:"current_bytes,omitempty"`
	TotalBytes   uint64 `json:"total_bytes,omitempty"`
}

func imageRefLooksUntagged(ref, imageID string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return true
	}
	trimmedID := strings.TrimSpace(imageID)
	if trimmedID != "" && ref == trimmedID {
		return true
	}
	return strings.HasPrefix(ref, "sha256:") || strings.Contains(ref, "@sha256:")
}

func pluralizeImageAge(value int, unit string) string {
	if value == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", value, unit)
}
