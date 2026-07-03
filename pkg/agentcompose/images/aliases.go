package images

import owner "agent-compose/pkg/images"

const (
	DefaultDockerPingTimeout  = owner.DefaultDockerPingTimeout
	ErrorKindConflict         = owner.ErrorKindConflict
	ErrorKindInternal         = owner.ErrorKindInternal
	ErrorKindInvalidReference = owner.ErrorKindInvalidReference
	ErrorKindNotFound         = owner.ErrorKindNotFound
	ErrorKindUnavailable      = owner.ErrorKindUnavailable
	ErrorKindUnknown          = owner.ErrorKindUnknown
)

type (
	AutoBackend         = owner.AutoBackend
	AutoBackendOption   = owner.AutoBackendOption
	Backend             = owner.Backend
	DockerBackend       = owner.DockerBackend
	DockerBackendOption = owner.DockerBackendOption
	DockerClient        = owner.DockerClient
	DockerClientFactory = owner.DockerClientFactory
	DockerPingFunc      = owner.DockerPingFunc
	EnsureRequest       = owner.EnsureRequest
	ErrorKind           = owner.ErrorKind
	InspectRequest      = owner.InspectRequest
	InspectResult       = owner.InspectResult
	ListRequest         = owner.ListRequest
	ListResult          = owner.ListResult
	OCIBackend          = owner.OCIBackend
	OCIBackendOption    = owner.OCIBackendOption
	OpError             = owner.OpError
	PullRequest         = owner.PullRequest
	PullResult          = owner.PullResult
	RemoveRequest       = owner.RemoveRequest
	RemoveResult        = owner.RemoveResult
)

var (
	ClassifyBackendError           = owner.ClassifyBackendError
	CleanDockerRefs                = owner.CleanDockerRefs
	CleanOCIRefs                   = owner.CleanOCIRefs
	CloneStringMap                 = owner.CloneStringMap
	ConsumeDockerImagePullProgress = owner.ConsumeDockerImagePullProgress
	DockerEndpointFromEnv          = owner.DockerEndpointFromEnv
	DockerImageDangling            = owner.DockerImageDangling
	DockerInspectToProtoImage      = owner.DockerInspectToProtoImage
	DockerPlatformString           = owner.DockerPlatformString
	DockerSummaryToProtoImage      = owner.DockerSummaryToProtoImage
	EnsureDriverImage              = owner.EnsureDriverImage
	EnsureProjectAgentImages       = owner.EnsureProjectAgentImages
	FirstNonEmpty                  = owner.FirstNonEmpty
	FirstString                    = owner.FirstString
	ImageCachePlatform             = owner.ImageCachePlatform
	IsNotFound                     = owner.IsNotFound
	NewAutoBackend                 = owner.NewAutoBackend
	NewDockerBackend               = owner.NewDockerBackend
	NewOCIBackend                  = owner.NewOCIBackend
	NonNegativeUint64              = owner.NonNegativeUint64
	OCIMetadataToProtoImage        = owner.OCIMetadataToProtoImage
	PaginateProtoImages            = owner.PaginateProtoImages
	PingDockerDaemon               = owner.PingDockerDaemon
	TimeString                     = owner.TimeString
	UnixSecondsString              = owner.UnixSecondsString
	WithDockerClientFactory        = owner.WithDockerClientFactory
	WithDockerClock                = owner.WithDockerClock
	WithDockerPing                 = owner.WithDockerPing
	WithDockerPingTimeout          = owner.WithDockerPingTimeout
	WithOCIClock                   = owner.WithOCIClock
)
