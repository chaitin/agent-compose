package agentcompose

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) ListArtifacts(ctx context.Context, req *connect.Request[agentcomposev2.ListArtifactsRequest]) (*connect.Response[agentcomposev2.ListArtifactsResponse], error) {
	runs, err := s.resolveArtifactRuns(ctx, req.Msg.GetProjectId(), req.Msg.GetRunId())
	if err != nil {
		return nil, err
	}
	artifacts := make([]*agentcomposev2.Artifact, 0)
	for _, run := range runs {
		artifacts = append(artifacts, projectRunArtifacts(run)...)
	}
	return connect.NewResponse(paginateArtifacts(artifacts, req.Msg.GetOffset(), req.Msg.GetLimit())), nil
}

func (s *Service) GetArtifact(ctx context.Context, req *connect.Request[agentcomposev2.GetArtifactRequest]) (*connect.Response[agentcomposev2.GetArtifactResponse], error) {
	run, err := s.resolveArtifactLookupRun(ctx, req.Msg.GetArtifactId(), req.Msg.GetRunId())
	if err != nil {
		return nil, err
	}
	artifact, ok := findProjectRunArtifact(run, req.Msg.GetArtifactId(), req.Msg.GetPath())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("artifact not found"))
	}
	return connect.NewResponse(&agentcomposev2.GetArtifactResponse{Artifact: artifact}), nil
}

func (s *Service) ReadArtifact(ctx context.Context, req *connect.Request[agentcomposev2.ReadArtifactRequest]) (*connect.Response[agentcomposev2.ReadArtifactResponse], error) {
	run, err := s.resolveArtifactLookupRun(ctx, req.Msg.GetArtifactId(), req.Msg.GetRunId())
	if err != nil {
		return nil, err
	}
	artifact, ok := findProjectRunArtifact(run, req.Msg.GetArtifactId(), req.Msg.GetPath())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("artifact not found"))
	}
	if err := ensureArtifactReadableFile(run.ArtifactsDir, artifact.GetPath()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	content, readErr := os.ReadFile(artifact.GetPath())
	if readErr != nil {
		return nil, connect.NewError(connect.CodeInternal, readErr)
	}
	return connect.NewResponse(&agentcomposev2.ReadArtifactResponse{Artifact: artifact, Content: content}), nil
}

func (s *Service) WriteArtifact(ctx context.Context, req *connect.Request[agentcomposev2.WriteArtifactRequest]) (*connect.Response[agentcomposev2.WriteArtifactResponse], error) {
	run, err := s.resolveArtifactRun(ctx, "", req.Msg.GetRunId())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if err := validateArtifactName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(run.ArtifactsDir) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run artifacts_dir is required"))
	}
	relPath, err := cleanArtifactRelativePath(firstNonEmpty(req.Msg.GetPath(), name))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	artifactPath, err := artifactPathUnderDir(run.ArtifactsDir, relPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := ensureArtifactParentInDir(run.ArtifactsDir, artifactPath); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if info, err := os.Lstat(artifactPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("artifact path must be a regular file"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := os.WriteFile(artifactPath, req.Msg.GetContent(), 0o644); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	run.ResultJSON = upsertProjectRunArtifactResultJSON(run.ResultJSON, name, artifactPath)
	updated, err := s.configDB.UpdateProjectRun(ctx, run)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	artifact := artifactFromPath(updated, "cell."+name, artifactPath, "cell")
	if contentType := strings.TrimSpace(req.Msg.GetContentType()); contentType != "" {
		artifact.ContentType = contentType
	}
	for key, value := range req.Msg.GetMetadata() {
		if artifact.Metadata == nil {
			artifact.Metadata = map[string]string{}
		}
		artifact.Metadata[key] = value
	}
	return connect.NewResponse(&agentcomposev2.WriteArtifactResponse{Artifact: artifact}), nil
}

func (s *Service) DeleteArtifact(ctx context.Context, req *connect.Request[agentcomposev2.DeleteArtifactRequest]) (*connect.Response[agentcomposev2.DeleteArtifactResponse], error) {
	run, err := s.resolveArtifactLookupRun(ctx, req.Msg.GetArtifactId(), req.Msg.GetRunId())
	if err != nil {
		return nil, err
	}
	artifact, ok := findProjectRunArtifact(run, req.Msg.GetArtifactId(), req.Msg.GetPath())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("artifact not found"))
	}
	if err := ensureArtifactPathInDir(run.ArtifactsDir, artifact.GetPath()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	info, statErr := os.Lstat(artifact.GetPath())
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, connect.NewError(connect.CodeInternal, statErr)
	}
	if statErr == nil && info.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("artifact directory cannot be deleted through DeleteArtifact"))
	}
	if statErr == nil {
		if removeErr := os.Remove(artifact.GetPath()); removeErr != nil {
			return nil, connect.NewError(connect.CodeInternal, removeErr)
		}
	}
	run.ResultJSON = removeProjectRunArtifactResultJSON(run.ResultJSON, artifact.GetName(), artifact.GetPath())
	if _, err := s.configDB.UpdateProjectRun(ctx, run); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.DeleteArtifactResponse{Deleted: true}), nil
}

func (s *Service) PublishEvent(ctx context.Context, req *connect.Request[agentcomposev2.PublishEventRequest]) (*connect.Response[agentcomposev2.PublishEventResponse], error) {
	if s == nil || s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	topic := strings.TrimSpace(req.Msg.GetTopic())
	payloadJSON := strings.TrimSpace(req.Msg.GetPayloadJson())
	if topic == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("topic is required"))
	}
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	if !json.Valid([]byte(payloadJSON)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("payload_json must be valid JSON"))
	}
	created, err := s.configDB.CreateEvent(ctx, TopicEventRecord{
		Topic:          topic,
		Source:         TopicEventSourceSystem,
		CorrelationID:  firstNonEmpty(req.Msg.GetRuntimeContext().GetTraceId(), projectID),
		PayloadJSON:    payloadJSON,
		DispatchStatus: TopicEventDispatchPending,
		PublisherType:  "project",
		PublisherID:    projectID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev2.PublishEventResponse{Event: topicEventRecordResponse(created)}), nil
}

func (s *Service) ListEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListEventsRequest]) (*connect.Response[agentcomposev2.ListEventsResponse], error) {
	if s == nil || s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	limit := boundedProtoLimit(req.Msg.GetLimit(), 100, 500)
	filter := TopicEventFilter{
		ProjectID: strings.TrimSpace(req.Msg.GetProjectId()),
		RunID:     strings.TrimSpace(req.Msg.GetRunId()),
		Topic:     strings.TrimSpace(req.Msg.GetTopic()),
		Offset:    int(req.Msg.GetOffset()),
		Limit:     limit + 1,
	}
	items, err := s.configDB.ListEvents(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	totalCount := req.Msg.GetOffset() + uint32(len(items))
	if hasMore {
		totalCount = req.Msg.GetOffset() + uint32(limit) + 1
	}
	resp := &agentcomposev2.ListEventsResponse{
		Events:     topicEventRecordResponses(items),
		TotalCount: totalCount,
		HasMore:    hasMore,
		NextOffset: req.Msg.GetOffset(),
	}
	if hasMore {
		resp.NextOffset = req.Msg.GetOffset() + uint32(limit)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) WatchEvents(context.Context, *connect.Request[agentcomposev2.WatchEventsRequest], *connect.ServerStream[agentcomposev2.EventRecord]) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("event watch is not implemented"))
}

func (s *Service) runDetailResponse(ctx context.Context, run ProjectRunRecord) *agentcomposev2.RunDetail {
	detail := runDetailResponse(run)
	if s == nil || s.configDB == nil {
		return detail
	}
	items, err := s.configDB.ListEvents(ctx, TopicEventFilter{RunID: run.RunID, Limit: 100})
	if err == nil {
		detail.Events = topicEventRecordResponses(items)
	}
	return detail
}

func (s *Service) resolveArtifactRun(ctx context.Context, projectID, runID string) (ProjectRunRecord, error) {
	if s == nil || s.configDB == nil {
		return ProjectRunRecord{}, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ProjectRunRecord{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	run, err := s.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, err)
		}
		return ProjectRunRecord{}, connect.NewError(connect.CodeInternal, err)
	}
	if projectID = strings.TrimSpace(projectID); projectID != "" && run.ProjectID != projectID {
		return ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found in project %s", runID, projectID))
	}
	return run, nil
}

func (s *Service) resolveArtifactRuns(ctx context.Context, projectID, runID string) ([]ProjectRunRecord, error) {
	if strings.TrimSpace(runID) != "" {
		run, err := s.resolveArtifactRun(ctx, projectID, runID)
		if err != nil {
			return nil, err
		}
		return []ProjectRunRecord{run}, nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project id or run id is required"))
	}
	runs, err := s.configDB.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, Limit: 200})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return runs, nil
}

func (s *Service) resolveArtifactLookupRun(ctx context.Context, artifactID string, runID string) (ProjectRunRecord, error) {
	if strings.TrimSpace(runID) == "" {
		if parsedRunID := artifactRunIDFromArtifactID(artifactID); parsedRunID != "" {
			runID = parsedRunID
		}
	}
	return s.resolveArtifactRun(ctx, "", runID)
}

func artifactRunIDFromArtifactID(artifactID string) string {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return ""
	}
	if runID, _, ok := strings.Cut(artifactID, ":"); ok {
		return strings.TrimSpace(runID)
	}
	return ""
}

func projectRunArtifactsAndMetrics(run ProjectRunRecord) ([]*agentcomposev2.Artifact, map[string]string) {
	artifacts := projectRunArtifacts(run)
	metrics := projectRunMetrics(run)
	if len(metrics) == 0 {
		metrics = nil
	}
	return artifacts, metrics
}

func projectRunArtifacts(run ProjectRunRecord) []*agentcomposev2.Artifact {
	entries := projectRunArtifactEntries(run)
	artifacts := make([]*agentcomposev2.Artifact, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.name == "" || entry.path == "" {
			continue
		}
		key := entry.name + "\x00" + entry.path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		artifacts = append(artifacts, artifactFromPath(run, entry.name, entry.path, entry.scope))
	}
	sort.SliceStable(artifacts, func(i, j int) bool { return artifacts[i].GetName() < artifacts[j].GetName() })
	if len(artifacts) == 0 {
		return nil
	}
	return artifacts
}

func projectRunMetrics(run ProjectRunRecord) map[string]string {
	var payload struct {
		Metrics map[string]string `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.ResultJSON)), &payload); err != nil {
		return nil
	}
	return payload.Metrics
}

type projectRunArtifactEntry struct {
	name  string
	path  string
	scope string
}

func projectRunArtifactEntries(run ProjectRunRecord) []projectRunArtifactEntry {
	entries := make([]projectRunArtifactEntry, 0, 8)
	appendEntry := func(scope, name, artifactPath string) {
		artifactPath = strings.TrimSpace(artifactPath)
		if artifactPath == "" {
			return
		}
		entries = append(entries, projectRunArtifactEntry{name: scope + "." + name, path: artifactPath, scope: scope})
	}
	if run.LogsPath != "" {
		appendEntry("run", "logs", run.LogsPath)
	}
	if run.ArtifactsDir != "" {
		appendEntry("run", "artifactsDir", run.ArtifactsDir)
	}
	var payload struct {
		Artifacts        map[string]string `json:"artifacts"`
		RuntimeArtifacts map[string]string `json:"runtimeArtifacts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.ResultJSON)), &payload); err != nil {
		return entries
	}
	for name, artifactPath := range payload.Artifacts {
		appendEntry("cell", name, artifactPath)
	}
	for name, artifactPath := range payload.RuntimeArtifacts {
		appendEntry("runtime", name, artifactPath)
	}
	return entries
}

func artifactFromPath(run ProjectRunRecord, name string, artifactPath string, scope string) *agentcomposev2.Artifact {
	artifact := &agentcomposev2.Artifact{
		ArtifactId: run.RunID + ":" + name,
		RunId:      run.RunID,
		ProjectId:  run.ProjectID,
		Name:       name,
		Path:       artifactPath,
		CreatedAt:  formatProjectTime(run.CompletedAt),
		Metadata:   map[string]string{"scope": scope},
	}
	info, err := os.Stat(artifactPath)
	if err == nil {
		artifact.SizeBytes = uint64(info.Size())
		artifact.CreatedAt = formatProjectTime(info.ModTime())
		if !info.IsDir() {
			artifact.Digest = fileSHA256(artifactPath)
			artifact.ContentType = mime.TypeByExtension(filepath.Ext(artifactPath))
		}
	}
	return artifact
}

func findProjectRunArtifact(run ProjectRunRecord, artifactID string, artifactPath string) (*agentcomposev2.Artifact, bool) {
	artifactID = strings.TrimSpace(artifactID)
	artifactPath = strings.TrimSpace(artifactPath)
	for _, artifact := range projectRunArtifacts(run) {
		if artifactID != "" && artifact.GetArtifactId() == artifactID {
			return artifact, true
		}
		if artifactPath != "" && artifact.GetPath() == artifactPath {
			return artifact, true
		}
	}
	return nil, false
}

func validateArtifactName(name string) error {
	if name == "" {
		return fmt.Errorf("artifact name is required")
	}
	if strings.ContainsAny(name, "/\\:") || name == "." || name == ".." {
		return fmt.Errorf("artifact name must be a stable relative identifier")
	}
	return nil
}

func cleanArtifactRelativePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("artifact path is required")
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("artifact path must be relative")
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path must stay under artifacts_dir")
	}
	return cleaned, nil
}

func artifactPathUnderDir(root string, relPath string) (string, error) {
	rootAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	joinedAbs, err := filepath.Abs(filepath.Join(rootAbs, relPath))
	if err != nil {
		return "", err
	}
	if !pathIsWithinRoot(rootAbs, joinedAbs) {
		return "", fmt.Errorf("artifact path must stay under artifacts_dir")
	}
	return joinedAbs, nil
}

func ensureArtifactPathInDir(root string, artifactPath string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("run artifacts_dir is required")
	}
	rootAbs, pathAbs, err := artifactRootAndPathAbs(root, artifactPath)
	if err != nil {
		return err
	}
	if !pathIsWithinRoot(rootAbs, pathAbs) {
		return fmt.Errorf("artifact path must stay under artifacts_dir")
	}
	return nil
}

func ensureArtifactParentInDir(root string, artifactPath string) error {
	rootAbs, pathAbs, err := artifactRootAndPathAbs(root, artifactPath)
	if err != nil {
		return err
	}
	rootEval, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	parentEval, err := filepath.EvalSymlinks(filepath.Dir(pathAbs))
	if err != nil {
		return err
	}
	if !pathIsWithinRoot(rootEval, filepath.Join(parentEval, filepath.Base(pathAbs))) {
		return fmt.Errorf("artifact path must stay under artifacts_dir")
	}
	return nil
}

func ensureArtifactReadableFile(root string, artifactPath string) error {
	if err := ensureArtifactPathInDir(root, artifactPath); err != nil {
		return err
	}
	if err := ensureArtifactParentInDir(root, artifactPath); err != nil {
		return err
	}
	info, err := os.Lstat(artifactPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return fmt.Errorf("artifact path must be a regular file")
	}
	return nil
}

func artifactRootAndPathAbs(root string, artifactPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", "", err
	}
	pathAbs, err := filepath.Abs(strings.TrimSpace(artifactPath))
	if err != nil {
		return "", "", err
	}
	return rootAbs, pathAbs, nil
}

func pathIsWithinRoot(root string, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func upsertProjectRunArtifactResultJSON(resultJSON string, name string, artifactPath string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(resultJSON)), &payload); err != nil || payload == nil {
		payload = map[string]any{}
	}
	artifacts := stringMapFromAny(payload["artifacts"])
	artifacts[name] = artifactPath
	payload["artifacts"] = artifacts
	encoded, err := json.Marshal(payload)
	if err != nil {
		return resultJSON
	}
	return string(encoded)
}

func removeProjectRunArtifactResultJSON(resultJSON string, artifactName string, artifactPath string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(resultJSON)), &payload); err != nil || payload == nil {
		return firstNonEmpty(strings.TrimSpace(resultJSON), "{}")
	}
	artifacts := stringMapFromAny(payload["artifacts"])
	trimmedName := strings.TrimPrefix(strings.TrimSpace(artifactName), "cell.")
	for name, path := range artifacts {
		if name == trimmedName || path == artifactPath {
			delete(artifacts, name)
		}
	}
	if len(artifacts) == 0 {
		delete(payload, "artifacts")
	} else {
		payload["artifacts"] = artifacts
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return resultJSON
	}
	return string(encoded)
}

func stringMapFromAny(value any) map[string]string {
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			result[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if str, ok := item.(string); ok {
				result[key] = str
			}
		}
	}
	return result
}

func paginateArtifacts(items []*agentcomposev2.Artifact, offset uint32, limitRaw uint32) *agentcomposev2.ListArtifactsResponse {
	limit := boundedProtoLimit(limitRaw, 100, 500)
	total := len(items)
	start := int(offset)
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	resp := &agentcomposev2.ListArtifactsResponse{
		Artifacts:  items[start:end],
		TotalCount: uint32(total),
		HasMore:    end < total,
		NextOffset: uint32(end),
	}
	if !resp.HasMore {
		resp.NextOffset = 0
	}
	return resp
}

func boundedProtoLimit(raw uint32, defaultLimit int, maxLimit int) int {
	limit := int(raw)
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func topicEventRecordResponses(items []TopicEventRecord) []*agentcomposev2.EventRecord {
	responses := make([]*agentcomposev2.EventRecord, 0, len(items))
	for _, item := range items {
		responses = append(responses, topicEventRecordResponse(item))
	}
	return responses
}

func topicEventRecordResponse(item TopicEventRecord) *agentcomposev2.EventRecord {
	return &agentcomposev2.EventRecord{
		EventId:     item.ID,
		ProjectId:   item.PublisherID,
		RunId:       item.PublisherRunID,
		Topic:       item.Topic,
		PayloadJson: item.PayloadJSON,
		CreatedAt:   formatProjectTime(item.CreatedAt),
		Metadata: map[string]string{
			"sequence":       fmt.Sprintf("%d", item.Sequence),
			"source":         item.Source,
			"provider":       item.Provider,
			"correlationId":  item.CorrelationID,
			"dispatchStatus": item.DispatchStatus,
			"publisherType":  item.PublisherType,
		},
	}
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
