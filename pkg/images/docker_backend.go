package images

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	buildtypes "github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type DockerClient interface {
	ImageList(context.Context, typesimage.ListOptions) ([]typesimage.Summary, error)
	ImagePull(context.Context, string, typesimage.PullOptions) (io.ReadCloser, error)
	ImageBuild(context.Context, io.Reader, buildtypes.ImageBuildOptions) (buildtypes.ImageBuildResponse, error)
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (typesimage.InspectResponse, error)
	ImageRemove(context.Context, string, typesimage.RemoveOptions) ([]typesimage.DeleteResponse, error)
	DaemonHost() string
	Close() error
}

type DockerClientFactory func() (DockerClient, error)

type DockerBackendOption func(*DockerBackend)

type DockerBackend struct {
	newClient DockerClientFactory
	now       func() time.Time
}

func NewDockerBackend(options ...DockerBackendOption) *DockerBackend {
	backend := &DockerBackend{
		newClient: func() (DockerClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
		now: time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(backend)
		}
	}
	return backend
}

func WithDockerClientFactory(factory DockerClientFactory) DockerBackendOption {
	return func(backend *DockerBackend) {
		backend.newClient = factory
	}
}

func WithDockerClock(now func() time.Time) DockerBackendOption {
	return func(backend *DockerBackend) {
		backend.now = now
	}
}

func (b *DockerBackend) ListImages(ctx context.Context, req ListRequest) (ListResult, error) {
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ListResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	options := typesimage.ListOptions{All: req.All, SharedSize: true}
	if query := strings.TrimSpace(req.Query); query != "" {
		options.Filters = filters.NewArgs(filters.Arg("reference", query))
	}
	images, err := dockerClient.ImageList(ctx, options)
	if err != nil {
		return ListResult{}, OpError{Op: "list images", Endpoint: endpoint, Err: err}
	}
	result := make([]*agentcomposev2.Image, 0, len(images))
	for _, image := range images {
		result = append(result, DockerSummaryToProtoImage(image, b.inspectedAt(), ""))
	}
	return ListResult{
		Images: result,
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerBackend) PullImage(ctx context.Context, req PullRequest) (PullResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return PullResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	reader, err := dockerClient.ImagePull(ctx, imageRef, typesimage.PullOptions{Platform: DockerPlatformString(req.Platform)})
	if err != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	progress, err := ConsumeDockerImagePullProgress(reader)
	closeErr := reader.Close()
	if err != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	if closeErr != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: closeErr}
	}

	inspect, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return PullResult{}, OpError{Op: "inspect pulled image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	image := DockerInspectToProtoImage(inspect, b.inspectedAt(), imageRef)
	return PullResult{
		Image:       image,
		ResolvedRef: FirstNonEmpty(image.GetResolvedRef(), imageRef),
		Progress:    progress,
	}, nil
}

func (b *DockerBackend) InspectImage(ctx context.Context, req InspectRequest) (InspectResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return InspectResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	image, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return InspectResult{}, OpError{Op: "inspect image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	return InspectResult{
		Image: DockerInspectToProtoImage(image, b.inspectedAt(), imageRef),
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerBackend) RemoveImage(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return RemoveResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	deleted, err := dockerClient.ImageRemove(ctx, imageRef, typesimage.RemoveOptions{
		Force:         req.Force,
		PruneChildren: req.PruneChildren,
	})
	if err != nil {
		return RemoveResult{}, OpError{Op: "remove image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	result := RemoveResult{ImageRef: imageRef}
	for _, item := range deleted {
		if item.Untagged != "" {
			result.UntaggedRefs = append(result.UntaggedRefs, item.Untagged)
		}
		if item.Deleted != "" {
			result.DeletedIDs = append(result.DeletedIDs, item.Deleted)
		}
	}
	slices.Sort(result.UntaggedRefs)
	slices.Sort(result.DeletedIDs)
	return result, nil
}

func (b *DockerBackend) BuildImage(ctx context.Context, req BuildRequest, sink BuildEventSink) (BuildResult, error) {
	buildReq, err := normalizeBuildRequest(req)
	if err != nil {
		return BuildResult{}, OpError{Op: "build image", Err: err}
	}
	imageRef := FirstString(buildReq.Tags)

	dockerClient, endpoint, err := b.client()
	if err != nil {
		return BuildResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	contextReader, err := dockerBuildContext(buildReq.ContextDir, buildReq.Dockerfile)
	if err != nil {
		return BuildResult{}, OpError{Op: "prepare build context", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	defer func() { _ = contextReader.Close() }()

	if sink != nil {
		if err := sink.Send(&agentcomposev2.BuildImageEvent{
			Status:   agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING,
			Stage:    "build",
			Message:  "Building " + imageRef,
			ImageRef: imageRef,
		}); err != nil {
			return BuildResult{}, err
		}
	}

	resp, err := dockerClient.ImageBuild(ctx, contextReader, dockerBuildOptions(buildReq))
	if err != nil {
		return BuildResult{}, OpError{Op: "build image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	progressErr := consumeDockerBuildEvents(resp.Body, sink, imageRef)
	closeErr := resp.Body.Close()
	if progressErr != nil {
		return BuildResult{}, OpError{Op: "build image", Endpoint: endpoint, ImageRef: imageRef, Err: progressErr}
	}
	if closeErr != nil {
		return BuildResult{}, OpError{Op: "build image", Endpoint: endpoint, ImageRef: imageRef, Err: closeErr}
	}

	inspect, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return BuildResult{}, OpError{Op: "inspect built image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	image := DockerInspectToProtoImage(inspect, b.inspectedAt(), imageRef)
	result := BuildResult{
		Image:       image,
		ImageRef:    imageRef,
		ResolvedRef: FirstNonEmpty(image.GetResolvedRef(), imageRef),
	}
	if sink != nil {
		if err := sink.Send(&agentcomposev2.BuildImageEvent{
			Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
			Stage:       "completed",
			Message:     "Built " + imageRef,
			Image:       image,
			ImageRef:    result.ImageRef,
			ResolvedRef: result.ResolvedRef,
		}); err != nil {
			return BuildResult{}, err
		}
	}
	return result, nil
}

func normalizeBuildRequest(req BuildRequest) (BuildRequest, error) {
	contextDir := strings.TrimSpace(req.ContextDir)
	if contextDir == "" {
		contextDir = "."
	}
	absContext, err := filepath.Abs(contextDir)
	if err != nil {
		return BuildRequest{}, err
	}
	info, err := os.Stat(absContext)
	if err != nil {
		return BuildRequest{}, err
	}
	if !info.IsDir() {
		return BuildRequest{}, fmt.Errorf("context_dir must be a directory")
	}

	dockerfile := strings.TrimSpace(req.Dockerfile)
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	dockerfilePath := dockerfile
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(absContext, dockerfilePath)
	}
	if _, err := os.Stat(dockerfilePath); err != nil {
		return BuildRequest{}, err
	}
	relDockerfile, err := filepath.Rel(absContext, dockerfilePath)
	if err != nil {
		return BuildRequest{}, err
	}
	if strings.HasPrefix(relDockerfile, ".."+string(filepath.Separator)) || relDockerfile == ".." || filepath.IsAbs(relDockerfile) {
		return BuildRequest{}, fmt.Errorf("dockerfile must be inside context_dir")
	}

	tags := cleanBuildTags(req.Tags)
	if len(tags) == 0 {
		return BuildRequest{}, fmt.Errorf("at least one tag is required")
	}

	normalized := req
	normalized.ContextDir = absContext
	normalized.Dockerfile = filepath.ToSlash(relDockerfile)
	normalized.Tags = tags
	normalized.BuildArgs = cloneBuildArgs(req.BuildArgs)
	return normalized, nil
}

func dockerBuildContext(contextDir, dockerfile string) (io.ReadCloser, error) {
	excludes, err := readDockerignore(contextDir)
	if err != nil {
		return nil, err
	}
	return tarBuildContext(contextDir, dockerfile, excludes)
}

func readDockerignore(contextDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	patterns := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, nil
}

func tarBuildContext(contextDir, dockerfile string, excludes []string) (io.ReadCloser, error) {
	tmp, err := os.CreateTemp("", "agent-compose-build-context-*.tar")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			cleanup()
		}
	}()

	writer := tar.NewWriter(tmp)
	if err := filepath.WalkDir(contextDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if shouldExcludeBuildPath(rel, entry.IsDir(), dockerfile, excludes) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	success = true
	return cleanupReadCloser{ReadCloser: tmp, cleanup: cleanup}, nil
}

func shouldExcludeBuildPath(path string, isDir bool, dockerfile string, excludes []string) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	if path == ".dockerignore" || path == filepath.ToSlash(dockerfile) {
		return false
	}
	excluded := false
	for _, pattern := range excludes {
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		pattern = filepath.ToSlash(strings.Trim(strings.TrimSpace(pattern), "/"))
		if pattern == "" {
			continue
		}
		if buildIgnorePatternMatches(pattern, path, isDir) {
			excluded = !negated
		}
	}
	return excluded
}

func buildIgnorePatternMatches(pattern string, path string, isDir bool) bool {
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		base := filepath.Base(path)
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}
	if strings.HasSuffix(path, "/"+pattern) || path == pattern {
		return true
	}
	return isDir && strings.HasPrefix(path, strings.TrimSuffix(pattern, "/")+"/")
}

type cleanupReadCloser struct {
	io.ReadCloser
	cleanup func()
}

func (r cleanupReadCloser) Close() error {
	err := r.ReadCloser.Close()
	if r.cleanup != nil {
		r.cleanup()
	}
	return err
}

func dockerBuildOptions(req BuildRequest) buildtypes.ImageBuildOptions {
	return buildtypes.ImageBuildOptions{
		Tags:       req.Tags,
		Dockerfile: req.Dockerfile,
		BuildArgs:  dockerBuildArgs(req.BuildArgs),
		Target:     strings.TrimSpace(req.Target),
		Platform:   DockerPlatformString(req.Platform),
		NoCache:    req.NoCache,
		PullParent: req.Pull,
		Remove:     true,
	}
}

func dockerBuildArgs(args map[string]string) map[string]*string {
	if len(args) == 0 {
		return nil
	}
	result := make(map[string]*string, len(args))
	for key, value := range args {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value := value
		result[key] = &value
	}
	return result
}

func consumeDockerBuildEvents(reader io.Reader, sink BuildEventSink, imageRef string) error {
	decoder := json.NewDecoder(reader)
	for {
		var payload struct {
			Stream      string `json:"stream"`
			Status      string `json:"status"`
			ID          string `json:"id"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Aux map[string]any `json:"aux"`
		}
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if payload.Error != "" {
			return errors.New(strings.TrimSpace(payload.Error))
		}
		if payload.ErrorDetail != nil && strings.TrimSpace(payload.ErrorDetail.Message) != "" {
			return errors.New(strings.TrimSpace(payload.ErrorDetail.Message))
		}
		message := strings.TrimSpace(FirstNonEmpty(payload.Stream, payload.Status))
		if message == "" {
			continue
		}
		if sink != nil {
			if err := sink.Send(&agentcomposev2.BuildImageEvent{
				Status:   agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING,
				Stage:    FirstNonEmpty(payload.ID, "build"),
				Message:  message,
				ImageRef: imageRef,
			}); err != nil {
				return err
			}
		}
	}
}

func cleanBuildTags(tags []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result
}

func cloneBuildArgs(args map[string]string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	result := make(map[string]string, len(args))
	for key, value := range args {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func (b *DockerBackend) client() (DockerClient, string, error) {
	if b == nil || b.newClient == nil {
		return nil, "", OpError{Op: "connect docker daemon", Endpoint: DockerEndpointFromEnv(), Err: fmt.Errorf("docker image client factory is required")}
	}
	dockerClient, err := b.newClient()
	endpoint := DockerEndpointFromEnv()
	if dockerClient != nil && strings.TrimSpace(dockerClient.DaemonHost()) != "" {
		endpoint = dockerClient.DaemonHost()
	}
	if err != nil {
		return nil, "", OpError{Op: "connect docker daemon", Endpoint: endpoint, Err: err}
	}
	return dockerClient, endpoint, nil
}

func (b *DockerBackend) inspectedAt() string {
	now := time.Now
	if b != nil && b.now != nil {
		now = b.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}
