package capproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	SandboxTokenMetadata           = "x-capability-sandbox-token"
	deprecatedSessionTokenMetadata = "x-capability-session-token"
)

type SandboxBinding struct {
	SandboxID string
	// ManagedProjectID and ManagedAgentID scope project-managed capability
	// configuration. They remain empty for legacy and manually created sandboxes.
	ManagedProjectID string
	ManagedAgentID   string
	// CapsetIDs is the set of capsets the sandbox is allowed to use. The guest
	// picks one per call (x-octobus-capset); capproxy validates membership.
	CapsetIDs []string
}

type SandboxResolver interface {
	ResolveCapabilitySandbox(ctx context.Context, token string) (SandboxBinding, error)
}

// OctoBusResolver returns the current OctoBus dial target and token. ok is
// false when the gateway is not configured, so the data plane stays in sync
// with page edits without a restart.
type OctoBusResolver func(ctx context.Context) (addr string, token string, ok bool)

type Server struct {
	listen     string
	octobus    OctoBusResolver
	targets    TargetResolver
	sandboxes  SandboxResolver
	grpcServer *grpc.Server
}

type Config struct {
	Listen  string
	OctoBus OctoBusResolver
	Targets TargetResolver
}

func NewServer(config Config, sandboxes SandboxResolver) *Server {
	return &Server{
		listen:    strings.TrimSpace(config.Listen),
		octobus:   config.OctoBus,
		targets:   config.Targets,
		sandboxes: sandboxes,
	}
}

func (s *Server) Configured() bool {
	return s != nil && s.listen != "" && s.octobus != nil && s.sandboxes != nil
}

func (s *Server) Serve(ctx context.Context) error {
	if !s.Configured() {
		return nil
	}
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	return s.serve(ctx, ln)
}

func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	s.grpcServer = grpc.NewServer(grpc.ForceServerCodec(rawCodec{}), grpc.UnknownServiceHandler(s.handleUnknown))
	errCh := make(chan error, 1)
	go func() { errCh <- s.grpcServer.Serve(ln) }()
	select {
	case <-ctx.Done():
		s.grpcServer.GracefulStop()
		return nil
	case err := <-errCh:
		if err == nil {
			return nil
		}
		return err
	}
}

func (s *Server) handleUnknown(_ any, stream grpc.ServerStream) error {
	method, ok := grpc.MethodFromServerStream(stream)
	if !ok {
		return status.Error(codes.Internal, "missing gRPC method")
	}
	binding, err := s.resolveSandbox(stream.Context())
	if err != nil {
		return err
	}
	// The guest picks which capset this call targets (x-octobus-capset); capproxy
	// validates it is one the sandbox is allowed to use. Both the reflection and
	// business paths require a resolved capset.
	declaration, err := resolveCallCapset(stream.Context(), binding.CapsetIDs)
	if err != nil {
		return err
	}
	target, err := s.resolveTarget(stream.Context(), binding, declaration)
	if err != nil {
		return err
	}
	outgoing := buildOutgoingMetadata(stream.Context(), target.CapsetID)
	if !isReflectionMethod(method) {
		// Business calls route by capset + instance + method. The instance comes
		// from the injected guide.
		if firstMetadata(outgoing, "x-octobus-instance") == "" {
			return status.Error(codes.FailedPrecondition, "x-octobus-instance is required")
		}
	}
	return s.proxyStream(stream, method, outgoing, target)
}

// resolveCallCapset picks the capset for this call: the guest-supplied
// x-octobus-capset if it is in the allowed set, or the sole allowed capset when
// the guest omits it. Otherwise it is an error (the guest must disambiguate).
func resolveCallCapset(ctx context.Context, allowed []string) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	requested := firstMetadata(md, "x-octobus-capset")
	if requested != "" {
		if containsString(allowed, requested) {
			return requested, nil
		}
		return "", status.Errorf(codes.PermissionDenied, "capset %q is not allowed for this sandbox", requested)
	}
	if len(allowed) == 1 {
		return allowed[0], nil
	}
	return "", status.Error(codes.FailedPrecondition, "x-octobus-capset is required: sandbox allows multiple capsets")
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// buildOutgoingMetadata forwards the guest's incoming metadata to OctoBus,
// except agent-compose's own sandbox credential and any authorization (OctoBus
// auth is injected in proxyStream).
// x-octobus-capset is forced to the resolved, sandbox-allowed value so the guest
// cannot reach a capset outside its set.
func buildOutgoingMetadata(ctx context.Context, capset string) metadata.MD {
	incoming, _ := metadata.FromIncomingContext(ctx)
	outgoing := incoming.Copy()
	outgoing.Delete(SandboxTokenMetadata)
	outgoing.Delete(deprecatedSessionTokenMetadata)
	outgoing.Delete("authorization")
	outgoing.Set("x-octobus-capset", capset)
	return outgoing
}

func (s *Server) resolveSandbox(ctx context.Context) (SandboxBinding, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	token := firstMetadata(md, SandboxTokenMetadata)
	if token == "" {
		token = firstMetadata(md, deprecatedSessionTokenMetadata)
	}
	if token == "" {
		token = bearerToken(firstMetadata(md, "authorization"))
	}
	if token == "" {
		return SandboxBinding{}, status.Error(codes.Unauthenticated, "missing capability sandbox token")
	}
	binding, err := s.sandboxes.ResolveCapabilitySandbox(ctx, token)
	if err != nil {
		return SandboxBinding{}, status.Error(codes.Unauthenticated, err.Error())
	}
	if len(binding.CapsetIDs) == 0 {
		return SandboxBinding{}, status.Error(codes.FailedPrecondition, "sandbox has no capability capset")
	}
	return binding, nil
}

func (s *Server) proxyStream(client grpc.ServerStream, method string, outgoing metadata.MD, target Target) error {
	if target.Token != "" {
		outgoing.Set("authorization", "Bearer "+target.Token)
	}
	grpcTarget, transportCredentials, err := resolveGRPCTransport(target.Addr, nil)
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	conn, err := grpc.NewClient(grpcTarget, grpc.WithTransportCredentials(transportCredentials), grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	defer func() { _ = conn.Close() }()
	ctx := metadata.NewOutgoingContext(client.Context(), outgoing)
	desc := &grpc.StreamDesc{StreamName: strings.TrimPrefix(method, "/"), ServerStreams: true, ClientStreams: true}
	backend, err := conn.NewStream(ctx, desc, method)
	if err != nil {
		return err
	}
	errCh := make(chan error, 2)
	go func() {
		for {
			frame := rawFrame(nil)
			err := client.RecvMsg(&frame)
			if err == io.EOF {
				errCh <- backend.CloseSend()
				return
			}
			if err != nil {
				errCh <- err
				return
			}
			if err := backend.SendMsg(&frame); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		for {
			frame := rawFrame(nil)
			err := backend.RecvMsg(&frame)
			if err == io.EOF {
				errCh <- nil
				return
			}
			if err != nil {
				errCh <- err
				return
			}
			if err := client.SendMsg(&frame); err != nil {
				errCh <- err
				return
			}
		}
	}()
	var first error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && first == nil {
			first = err
		}
	}
	return first
}

func isReflectionMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.reflection.v1.") || strings.HasPrefix(method, "/grpc.reflection.v1alpha.")
}

func firstMetadata(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func bearerToken(value string) string {
	if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return ""
	}
	return strings.TrimSpace(value[len("bearer "):])
}

func resolveGRPCTransport(raw string, roots *x509.CertPool) (string, credentials.TransportCredentials, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, errors.New("octobus target is empty")
	}
	if !strings.Contains(raw, "://") {
		target := strings.TrimRight(raw, "/")
		if target == "" {
			return "", nil, errors.New("octobus target is empty")
		}
		return target, insecure.NewCredentials(), nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", nil, fmt.Errorf("parse octobus target: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("octobus target scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", nil, fmt.Errorf("octobus target must be an origin URL without userinfo, non-root path, query, or fragment")
	}
	if parsed.Scheme == "http" {
		return parsed.Host, insecure.NewCredentials(), nil
	}
	return parsed.Host, credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: parsed.Hostname(),
		RootCAs:    roots,
	}), nil
}

var _ encoding.Codec = rawCodec{}

type rawFrame []byte

type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch x := v.(type) {
	case *rawFrame:
		return []byte(*x), nil
	case rawFrame:
		return []byte(x), nil
	case proto.Message:
		return proto.Marshal(x)
	default:
		return nil, fmt.Errorf("unsupported raw marshal type %T", v)
	}
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	switch x := v.(type) {
	case *rawFrame:
		*x = append((*x)[:0], data...)
		return nil
	case proto.Message:
		return proto.Unmarshal(data, x)
	default:
		return fmt.Errorf("unsupported raw unmarshal type %T", v)
	}
}
