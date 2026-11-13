package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type runInputStore interface {
	SaveRunInput(context.Context, domain.RunInputPreparation, domain.RunInputPreparation) error
}

type runInputWorkspace interface {
	OpenRunInput(context.Context, *domain.Sandbox, domain.RunInputPreparation) (*sandboxstore.RunInputWriter, error)
}

func (c *Controller) prepareRunInput(ctx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox, receive RunAttachReceiver) (input *domain.RunInputPreparation, resultErr error) {
	original := *run.InputPreparation
	input = &original
	evidence, ok := c.configDB.(runInputStore)
	if !ok {
		return input, fmt.Errorf("%w: input evidence store required", domain.ErrUnsupported)
	}
	workspace, ok := c.store.(runInputWorkspace)
	if !ok {
		return input, fmt.Errorf("%w: private input workspace required", domain.ErrUnsupported)
	}
	bound := *input
	bound.SandboxID = sandbox.Summary.ID
	bound.TemporaryNames = make([]string, len(bound.Files))
	for index := range bound.TemporaryNames {
		bound.TemporaryNames[index] = ".ac-upload-" + uuid.NewString()
	}
	// Persist ownership before creating even an empty temporary inode.
	if err := evidence.SaveRunInput(ctx, *input, bound); err != nil {
		return input, err
	}
	input = &bound
	writer, err := workspace.OpenRunInput(ctx, sandbox, bound)
	if err != nil {
		return input, err
	}
	defer func() { resultErr = errors.Join(resultErr, writer.Close()) }()
	if err := receiveRunInput(ctx, writer, receive); err != nil {
		return input, err
	}
	if err := writer.Seal(ctx); err != nil {
		return input, err
	}
	input, err = publishRunInput(ctx, evidence, writer, *input)
	if err != nil {
		return input, err
	}
	return input, writer.Delivered()
}

func receiveRunInput(ctx context.Context, writer *sandboxstore.RunInputWriter, receive RunAttachReceiver) error {
	for {
		frame, err := receive()
		if err != nil {
			return err
		}
		switch frame.Kind {
		case RunAttachInputFileChunk:
			if len(frame.Data) > MaxInputChunkBytes {
				return fmt.Errorf("%w: file chunk exceeds limit", ErrInvalidRequest)
			}
			if err := writer.Write(ctx, frame.FileID, frame.Offset, frame.Data); err != nil {
				return err
			}
		case RunAttachInputComplete:
			return nil
		case RunAttachInputCancel:
			return context.Canceled
		case RunAttachInputUploadDisconnected:
			return fmt.Errorf("%w: file input disconnected before completion", ErrInvalidRequest)
		default:
			return fmt.Errorf("%w: expected file chunk or input completion", ErrInvalidRequest)
		}
	}
}

// collisionCandidate derives a non-conflicting publication name. It keeps a
// dotfile's leading dot attached to its name (".gitignore" → ".gitignore (1)"
// rather than " (1).gitignore") and otherwise mirrors the conventional
// "name (N).ext" rename.
func collisionCandidate(name string, suffix int) string {
	if suffix == 0 {
		return name
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	if base == "" {
		return fmt.Sprintf("%s (%d)", name, suffix)
	}
	return fmt.Sprintf("%s (%d)%s", base, suffix, extension)
}

func publishRunInput(ctx context.Context, store runInputStore, writer *sandboxstore.RunInputWriter, input domain.RunInputPreparation) (*domain.RunInputPreparation, error) {
	input.Files = append([]domain.RunInputFile(nil), input.Files...)
	for index, file := range input.Files {
		for suffix := 0; ; suffix++ {
			candidate := collisionCandidate(file.Name, suffix)
			next := input
			next.Files = append([]domain.RunInputFile(nil), input.Files...)
			next.State = domain.RunInputStatePublishing
			next.Files[index].RelativePath = candidate
			if err := store.SaveRunInput(ctx, input, next); err != nil {
				return &input, err
			}
			input = next
			err := writer.Publish(ctx, index, candidate)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return &input, err
			}
			break
		}
	}
	if err := writer.Sync(); err != nil {
		return &input, err
	}
	ready := input
	ready.State = domain.RunInputStateReady
	if err := store.SaveRunInput(ctx, input, ready); err != nil {
		return &input, err
	}
	return &ready, nil
}

func promptWithInputFiles(prompt string, files []domain.RunInputFile) string {
	paths := make([]struct {
		Name string `json:"name"`
		Path string `json:"relative_path"`
	}, len(files))
	for i, file := range files {
		paths[i].Name, paths[i].Path = file.Name, file.RelativePath
	}
	encoded, err := json.Marshal(paths)
	if err != nil {
		return prompt
	} // All values are plain strings; Marshal cannot fail.
	return prompt + "\n\nAttached workspace files (JSON data):\n" + string(encoded)
}
