package dockerdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	pathpkg "path"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	distreference "github.com/distribution/reference"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func ensureDockerImage(ctx context.Context, imageRef string) (string, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return "", nil
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("connect docker daemon for guest image %s: %w", imageRef, err)
	}
	defer func() { _ = dockerClient.Close() }()

	if resolvedRef, ok, err := resolveLocalDockerImageRef(ctx, dockerClient, imageRef); err == nil && ok {
		return resolvedRef, nil
	} else if err != nil {
		return "", fmt.Errorf("inspect guest image %s: %w", imageRef, err)
	}

	reader, err := dockerClient.ImagePull(ctx, imageRef, typesimage.PullOptions{})
	if err != nil {
		return "", fmt.Errorf("pull guest image %s: %w", imageRef, err)
	}
	defer func() { _ = reader.Close() }()

	if err := consumeDockerPullStream(reader); err != nil {
		return "", fmt.Errorf("pull guest image %s: %w", imageRef, err)
	}

	if resolvedRef, ok, err := resolveLocalDockerImageRef(ctx, dockerClient, imageRef); err == nil && ok {
		return resolvedRef, nil
	} else if err != nil {
		return "", fmt.Errorf("inspect pulled guest image %s: %w", imageRef, err)
	}
	return imageRef, nil
}

func EnsureDockerImage(ctx context.Context, imageRef string) (string, error) {
	return ensureDockerImage(ctx, imageRef)
}

func resolveLocalDockerImageRef(ctx context.Context, dockerClient *client.Client, imageRef string) (string, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return "", false, nil
	}

	if _, err := dockerClient.ImageInspect(ctx, imageRef); err == nil {
		return imageRef, true, nil
	} else if !cerrdefs.IsNotFound(err) {
		return "", false, err
	}

	images, err := dockerClient.ImageList(ctx, typesimage.ListOptions{All: true})
	if err != nil {
		return "", false, err
	}
	resolvedRef, ok := matchLocalDockerImageRef(imageRef, images)
	return resolvedRef, ok, nil
}

type dockerImageRef struct {
	familiar    string
	path        string
	trimmedPath string
	basename    string
	tag         string
	digest      string
}

func matchLocalDockerImageRef(imageRef string, images []typesimage.Summary) (string, bool) {
	requested, err := parseDockerImageRef(imageRef)
	if err != nil {
		return "", false
	}

	bestRef := ""
	bestImageID := ""
	bestScore := 0
	ambiguous := false
	for _, image := range images {
		for _, candidateRef := range append(append([]string(nil), image.RepoTags...), image.RepoDigests...) {
			candidate, err := parseDockerImageRef(candidateRef)
			if err != nil {
				continue
			}
			score := scoreDockerImageRefMatch(requested, candidate)
			if score == 0 {
				continue
			}
			switch {
			case score > bestScore:
				bestRef = candidateRef
				bestImageID = image.ID
				bestScore = score
				ambiguous = false
			case score == bestScore:
				if strings.TrimSpace(bestImageID) == strings.TrimSpace(image.ID) {
					if bestRef == "" || len(candidateRef) < len(bestRef) {
						bestRef = candidateRef
					}
					continue
				}
				ambiguous = true
			}
		}
	}
	if bestScore == 0 || ambiguous {
		return "", false
	}
	return bestRef, true
}

func parseDockerImageRef(value string) (dockerImageRef, error) {
	value = strings.TrimSpace(value)
	named, err := distreference.ParseDockerRef(value)
	if err != nil {
		return dockerImageRef{}, err
	}
	info := dockerImageRef{
		familiar: distreference.FamiliarString(named),
		path:     distreference.Path(named),
	}
	info.trimmedPath = strings.TrimPrefix(info.path, "library/")
	info.basename = pathpkg.Base(info.trimmedPath)
	if tagged, ok := named.(distreference.Tagged); ok {
		info.tag = tagged.Tag()
	}
	if digested, ok := named.(distreference.Digested); ok {
		info.digest = digested.Digest().String()
	}
	return info, nil
}

func scoreDockerImageRefMatch(requested, candidate dockerImageRef) int {
	switch {
	case requested.digest != "":
		if requested.digest != candidate.digest {
			return 0
		}
	case requested.tag != candidate.tag:
		return 0
	}

	switch {
	case requested.familiar == candidate.familiar:
		return 120
	case requested.path == candidate.path:
		return 110
	case requested.trimmedPath == candidate.trimmedPath:
		return 100
	case requested.basename != "" && requested.basename == candidate.basename:
		return 80
	default:
		return 0
	}
}

func consumeDockerPullStream(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	for {
		var payload struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Status string `json:"status"`
			Stream string `json:"stream"`
		}
		if err := decoder.Decode(&payload); err != nil {
			if err == io.EOF {
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
	}
}
