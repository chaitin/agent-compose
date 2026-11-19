package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runinputs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

type uploadProjectRunStore struct {
	RunStore
}

func (uploadProjectRunStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	if id != "project-1" {
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", id, "project not found", nil)
	}
	return domain.ProjectRecord{ID: id}, nil
}

func newUploadClient(t *testing.T) (agentcomposev2connect.RunServiceClient, *runinputs.Store) {
	t.Helper()
	store, err := runinputs.Open(runinputs.Options{
		Root: t.TempDir(), MaxObjectBytes: 1 << 20, MaxStoreBytes: 8 << 20, TTL: 15 * time.Minute,
		FreeSpace: func(string) (uint64, uint64, error) { return 1 << 40, 1 << 40, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRunHandler(nil, uploadProjectRunStore{}).WithRunInputUploads(store)
	mux := http.NewServeMux()
	path, serviceHandler := agentcomposev2connect.NewRunServiceHandler(handler, UploadRunInputHandlerOptions()...)
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL), store
}

func sendUpload(t *testing.T, client agentcomposev2connect.RunServiceClient, frames ...*agentcomposev2.UploadRunInputRequest) (*agentcomposev2.UploadRunInputResponse, error) {
	t.Helper()
	stream := client.UploadRunInput(context.Background())
	for _, frame := range frames {
		if err := stream.Send(frame); err != nil {
			break
		}
	}
	response, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

func infoFrame(project string, data []byte) *agentcomposev2.UploadRunInputRequest {
	sum := sha256.Sum256(data)
	return &agentcomposev2.UploadRunInputRequest{Frame: &agentcomposev2.UploadRunInputRequest_Info{Info: &agentcomposev2.RunInputUploadInfo{
		ProjectId: project, SizeBytes: uint64(len(data)), Sha256: hex.EncodeToString(sum[:]),
	}}}
}

func chunkFrame(data []byte) *agentcomposev2.UploadRunInputRequest {
	return &agentcomposev2.UploadRunInputRequest{Frame: &agentcomposev2.UploadRunInputRequest_Chunk{Chunk: data}}
}

func requireUploadError(t *testing.T, err error, code connect.Code, effect apierror.Effect) {
	t.Helper()
	if connect.CodeOf(err) != code {
		t.Fatalf("err = %v, want %v", err, code)
	}
	detail, ok := errdetail.From(err)
	if !ok || EffectFromProto(detail.Effect) != effect {
		t.Fatalf("err detail = %+v ok=%v, want effect %v", detail, ok, effect)
	}
}

func TestUploadRunInputStagesAndDeduplicates(t *testing.T) {
	client, store := newUploadClient(t)
	data := []byte("alpha,beta\n1,2\n")

	first, err := sendUpload(t, client, infoFrame("project-1", data), chunkFrame(data[:5]), chunkFrame(data[5:]))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if first.GetUploadId() == "" || first.GetDeduplicated() || first.GetExpiresAt() == nil {
		t.Fatalf("response = %+v", first)
	}
	object, err := store.Resolve("project-1", first.GetUploadId())
	if err != nil || object.SizeBytes != int64(len(data)) {
		t.Fatalf("resolve = %+v, %v", object, err)
	}

	// A client that already has the content may still pipeline chunks; the
	// daemon answers after info and the response must still arrive intact.
	again, err := sendUpload(t, client, infoFrame("project-1", data), chunkFrame(data[:5]), chunkFrame(data[5:]))
	if err != nil {
		t.Fatalf("dedup upload: %v", err)
	}
	if !again.GetDeduplicated() || again.GetUploadId() != first.GetUploadId() {
		t.Fatalf("dedup response = %+v, want the first upload", again)
	}
}

func TestUploadRunInputRejectsBadStreams(t *testing.T) {
	client, _ := newUploadClient(t)
	data := []byte("payload")
	oversized := make([]byte, MaxRunInputUploadChunkBytes+1)

	cases := map[string]struct {
		frames []*agentcomposev2.UploadRunInputRequest
		code   connect.Code
	}{
		"no frames":       {nil, connect.CodeInvalidArgument},
		"chunk first":     {[]*agentcomposev2.UploadRunInputRequest{chunkFrame(data)}, connect.CodeInvalidArgument},
		"duplicate info":  {[]*agentcomposev2.UploadRunInputRequest{infoFrame("project-1", data), infoFrame("project-1", data)}, connect.CodeInvalidArgument},
		"short upload":    {[]*agentcomposev2.UploadRunInputRequest{infoFrame("project-1", data), chunkFrame(data[:2])}, connect.CodeInvalidArgument},
		"digest mismatch": {[]*agentcomposev2.UploadRunInputRequest{infoFrame("project-1", []byte("PAYLOAD")), chunkFrame(data)}, connect.CodeInvalidArgument},
		"oversized chunk": {[]*agentcomposev2.UploadRunInputRequest{infoFrame("project-1", oversized), chunkFrame(oversized)}, connect.CodeInvalidArgument},
		"unknown project": {[]*agentcomposev2.UploadRunInputRequest{infoFrame("project-2", data), chunkFrame(data)}, connect.CodeNotFound},
		"missing project": {[]*agentcomposev2.UploadRunInputRequest{infoFrame("", data)}, connect.CodeInvalidArgument},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sendUpload(t, client, test.frames...)
			requireUploadError(t, err, test.code, apierror.EffectNotApplied)
		})
	}
}

func TestUploadRunInputRejectsOversizedFrameAtTheTransport(t *testing.T) {
	client, _ := newUploadClient(t)
	// Larger than the per-message read limit, so Connect rejects the frame
	// before the handler can see it. Nothing is staged either way.
	frame := make([]byte, MaxRunInputUploadMessageBytes+1)
	_, err := sendUpload(t, client, infoFrame("project-1", frame), chunkFrame(frame))
	requireUploadError(t, err, connect.CodeResourceExhausted, apierror.EffectNotApplied)
}

func TestUploadRunInputReportsExhaustedStore(t *testing.T) {
	client, _ := newUploadClient(t)
	// The project share is a quarter of the 8 MiB store.
	for index := range 2 {
		data := make([]byte, 1<<20)
		data[0] = byte(index)
		if _, err := sendUpload(t, client, infoFrame("project-1", data), chunkFrame(data[:MaxRunInputUploadChunkBytes]),
			chunkFrame(data[MaxRunInputUploadChunkBytes:2*MaxRunInputUploadChunkBytes]),
			chunkFrame(data[2*MaxRunInputUploadChunkBytes:3*MaxRunInputUploadChunkBytes]),
			chunkFrame(data[3*MaxRunInputUploadChunkBytes:])); err != nil {
			t.Fatalf("upload %d: %v", index, err)
		}
	}
	data := []byte("one more")
	_, err := sendUpload(t, client, infoFrame("project-1", data), chunkFrame(data))
	requireUploadError(t, err, connect.CodeResourceExhausted, apierror.EffectNotApplied)
}

func TestUploadRunInputUnconfigured(t *testing.T) {
	handler := NewRunHandler(nil, uploadProjectRunStore{})
	_, err := handler.UploadRunInput(context.Background(), nil)
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("err = %v, want unimplemented", err)
	}
}
