package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"agent-compose/pkg/networks"
)

func TestDockerNetworkPlanKeepsBaselineWithoutIntent(t *testing.T) {
	runtime := &dockerRuntime{}
	dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatalf("NewClientWithOpts returned error: %v", err)
	}
	plan, err := runtime.networkPlanForSandbox(&Sandbox{}, dockerDaemonTopology{
		networkMode:   containerapi.NetworkMode("compose_default"),
		containerized: true,
		kind:          dockerDaemonContainerBridge,
	}, dockerClient)
	if err != nil {
		t.Fatalf("networkPlanForSandbox returned error: %v", err)
	}
	if plan.RequiresDaemonHost || plan.BaselineNetwork != "compose_default" {
		t.Fatalf("baseline plan = %#v", plan)
	}
}

func TestDockerNetworkPlanRejectsAdditionalBridgeForBridgeDaemon(t *testing.T) {
	runtime := &dockerRuntime{}
	dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatalf("NewClientWithOpts returned error: %v", err)
	}
	sandbox := &Sandbox{
		Summary: SandboxSummary{ID: "sandbox-1"},
		NetworkIntent: &SandboxNetworkIntent{
			ProjectID:   "project-1",
			AgentName:   "api",
			Definitions: []SandboxNetworkDefinition{{Name: "frontend", Driver: "bridge"}},
			Attachments: []string{"frontend"},
		},
	}
	_, err = runtime.networkPlanForSandbox(sandbox, dockerDaemonTopology{
		networkMode:   containerapi.NetworkMode("compose_default"),
		containerized: true,
		kind:          dockerDaemonContainerBridge,
	}, dockerClient)
	if err == nil || !strings.Contains(err.Error(), "must run natively or with Docker host networking") {
		t.Fatalf("networkPlanForSandbox error = %v", err)
	}
	for _, kind := range []dockerDaemonTopologyKind{dockerDaemonNative, dockerDaemonContainerHost} {
		plan, planErr := runtime.networkPlanForSandbox(sandbox, dockerDaemonTopology{networkMode: "default", kind: kind}, dockerClient)
		if planErr != nil || !plan.RequiresDaemonHost {
			t.Fatalf("networkPlanForSandbox kind %s = %#v, %v", kind, plan, planErr)
		}
	}
}

func TestDockerNetworkPlanRejectsRemoteAndUnknownDaemon(t *testing.T) {
	runtime := &dockerRuntime{}
	sandbox := &Sandbox{
		Summary: SandboxSummary{ID: "sandbox-1"},
		NetworkIntent: &SandboxNetworkIntent{
			ProjectID:   "project-1",
			AgentName:   "api",
			Definitions: []SandboxNetworkDefinition{{Name: "frontend", Driver: "bridge"}},
			Attachments: []string{"frontend"},
		},
	}
	remoteClient, err := client.NewClientWithOpts(client.WithHost("tcp://docker.example.test:2376"))
	if err != nil {
		t.Fatalf("NewClientWithOpts remote returned error: %v", err)
	}
	if _, err := runtime.networkPlanForSandbox(sandbox, dockerDaemonTopology{networkMode: "default", kind: dockerDaemonNative}, remoteClient); err == nil || !strings.Contains(err.Error(), "require a local Docker Engine") {
		t.Fatalf("remote Docker network plan error = %v", err)
	}

	localClient, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatalf("NewClientWithOpts local returned error: %v", err)
	}
	if _, err := runtime.networkPlanForSandbox(sandbox, dockerDaemonTopology{networkMode: "default", kind: dockerDaemonUnknown, detail: "self container unavailable"}, localClient); err == nil || !strings.Contains(err.Error(), "self container unavailable") {
		t.Fatalf("unknown topology network plan error = %v", err)
	}
}

func TestMergeDockerNetworkPortConfig(t *testing.T) {
	jupyterPort := nat.Port("8888/tcp")
	exposed, bindings, err := mergeDockerNetworkPortConfig(
		nat.PortSet{jupyterPort: {}},
		nat.PortMap{jupyterPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "40000"}}},
		&SandboxNetworkIntent{
			Expose: []string{"8080/tcp"},
			Ports:  []string{"127.0.0.1:18080:8080/tcp", "0.0.0.0:0:9090/tcp"},
		},
	)
	if err != nil {
		t.Fatalf("mergeDockerNetworkPortConfig returned error: %v", err)
	}
	if len(exposed) != 3 || len(bindings[jupyterPort]) != 1 {
		t.Fatalf("port config = %#v / %#v", exposed, bindings)
	}
	if got := bindings[nat.Port("8080/tcp")]; len(got) != 1 || got[0].HostIP != "127.0.0.1" || got[0].HostPort != "18080" {
		t.Fatalf("8080 bindings = %#v", got)
	}
	if got := bindings[nat.Port("9090/tcp")]; len(got) != 1 || got[0].HostIP != "0.0.0.0" || got[0].HostPort != "" {
		t.Fatalf("9090 bindings = %#v", got)
	}
}

func TestMergeDockerNetworkPortConfigRejectsInvalidCanonicalValues(t *testing.T) {
	tests := []struct {
		name   string
		intent *SandboxNetworkIntent
		want   string
	}{
		{name: "expose protocol", intent: &SandboxNetworkIntent{Expose: []string{"53/udp"}}, want: "invalid canonical container port"},
		{name: "expose range", intent: &SandboxNetworkIntent{Expose: []string{"70000/tcp"}}, want: "invalid canonical container port"},
		{name: "published shape", intent: &SandboxNetworkIntent{Ports: []string{"8080/tcp"}}, want: "invalid canonical published port"},
		{name: "published host port", intent: &SandboxNetworkIntent{Ports: []string{"127.0.0.1:70000:8080/tcp"}}, want: "invalid canonical host port"},
		{name: "published container port", intent: &SandboxNetworkIntent{Ports: []string{"127.0.0.1:0:0/tcp"}}, want: "invalid canonical container port"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := mergeDockerNetworkPortConfig(nil, nil, tc.intent); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mergeDockerNetworkPortConfig error = %v, want containing %q", err, tc.want)
			}
		})
	}
	if exposed, bindings, err := mergeDockerNetworkPortConfig(nil, nil, nil); err != nil || exposed != nil || bindings != nil {
		t.Fatalf("nil intent result = %#v / %#v / %v", exposed, bindings, err)
	}
}

func TestDockerSandboxNetworkStateIncludesPortOnlyBaseline(t *testing.T) {
	plan, err := networks.BuildPlan(networks.Intent{}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	port := nat.Port("8080/tcp")
	networkSettings := &containerapi.NetworkSettings{
		Networks: map[string]*networkapi.EndpointSettings{
			"bridge": {NetworkID: "network-1", IPAddress: "172.17.0.2"},
		},
	}
	networkSettings.Ports = nat.PortMap{
		port: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "49152"}},
	}
	state := dockerSandboxNetworkState(containerapi.InspectResponse{
		NetworkSettings: networkSettings,
	}, plan, true)
	if state == nil || state.Mode != string(networks.ModeSingleBridge) || len(state.Attachments) != 1 || state.Attachments[0].NetworkID != "network-1" {
		t.Fatalf("network state = %#v", state)
	}
	if len(state.PortBindings) != 1 || state.PortBindings[0].ContainerPort != "8080/tcp" || state.PortBindings[0].HostPort != "49152" {
		t.Fatalf("port binding state = %#v", state.PortBindings)
	}
	if dockerSandboxNetworkState(containerapi.InspectResponse{}, plan, false) != nil {
		t.Fatal("unconfigured baseline unexpectedly returned network state")
	}
}

func TestDockerContainerNetworkingConfigUsesPrimaryAttachment(t *testing.T) {
	if config := dockerContainerNetworkingConfig(networks.Plan{Mode: networks.ModeSingleBridge}); config != nil {
		t.Fatalf("single-bridge networking config = %#v, want nil", config)
	}
	plan := networks.Plan{Mode: networks.ModeMultiNetwork, Attachments: []networks.Attachment{
		{RuntimeName: "frontend", Aliases: []string{"api", "api-sandbox"}, GatewayPriority: 100},
		{RuntimeName: "backend", Aliases: []string{"api"}},
	}}
	config := dockerContainerNetworkingConfig(plan)
	if config == nil || len(config.EndpointsConfig) != 1 {
		t.Fatalf("multi-network config = %#v", config)
	}
	endpoint := config.EndpointsConfig["frontend"]
	if endpoint == nil || endpoint.GwPriority != 100 || len(endpoint.Aliases) != 2 || endpoint.Aliases[0] != "api" {
		t.Fatalf("primary endpoint = %#v", endpoint)
	}
	endpoint.Aliases[0] = "mutated"
	if plan.Attachments[0].Aliases[0] != "api" {
		t.Fatalf("networking config aliases mutated plan: %#v", plan.Attachments[0].Aliases)
	}
}

func TestValidateDockerManagedNetworkRejectsPropertyAndOwnershipDrift(t *testing.T) {
	attachment := networks.Attachment{
		RuntimeName: "project_frontend",
		Labels: map[string]string{
			networks.ManagedLabel:   "true",
			networks.ProjectIDLabel: "project-1",
		},
	}
	valid := networkapi.Inspect{
		Driver:     "bridge",
		Scope:      "local",
		EnableIPv4: true,
		Labels: map[string]string{
			networks.ManagedLabel:   "true",
			networks.ProjectIDLabel: "project-1",
		},
	}
	if err := validateDockerManagedNetwork(valid, attachment); err != nil {
		t.Fatalf("validateDockerManagedNetwork valid error = %v", err)
	}
	drifted := valid
	drifted.EnableIPv6 = true
	if err := validateDockerManagedNetwork(drifted, attachment); err == nil {
		t.Fatal("validateDockerManagedNetwork accepted IPv6 drift")
	}
	drifted = valid
	drifted.Labels = map[string]string{
		networks.ManagedLabel:   "true",
		networks.ProjectIDLabel: "other-project",
	}
	if err := validateDockerManagedNetwork(drifted, attachment); err == nil {
		t.Fatal("validateDockerManagedNetwork accepted ownership drift")
	}
}

func TestDockerManagedNetworkLifecycleViaAPI(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	inspectByName := map[string]networkapi.Inspect{}
	createCalls := 0
	connectCalls := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := req.URL.Path
		switch {
		case req.Method == http.MethodGet && strings.Contains(path, "/networks/"):
			name := path[strings.LastIndex(path, "/")+1:]
			mu.Lock()
			inspect, ok := inspectByName[name]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "network not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(inspect)
		case req.Method == http.MethodPost && strings.HasSuffix(path, "/networks/create"):
			var options struct {
				Name       string            `json:"Name"`
				Driver     string            `json:"Driver"`
				EnableIPv4 *bool             `json:"EnableIPv4"`
				Labels     map[string]string `json:"Labels"`
			}
			if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
				t.Errorf("decode network create request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			createCalls++
			inspectByName[options.Name] = networkapi.Inspect{
				Name:       options.Name,
				ID:         "id-" + options.Name,
				Driver:     options.Driver,
				Scope:      "local",
				EnableIPv4: options.EnableIPv4 != nil && *options.EnableIPv4,
				Labels:     options.Labels,
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(networkapi.CreateResponse{ID: "id-" + options.Name})
		case req.Method == http.MethodPost && strings.Contains(path, "/networks/") && strings.HasSuffix(path, "/connect"):
			parts := strings.Split(path, "/")
			name := parts[len(parts)-2]
			mu.Lock()
			connectCalls = append(connectCalls, name)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected Docker API request: %s %s", req.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dockerClient, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.43"))
	if err != nil {
		t.Fatalf("NewClientWithOpts returned error: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()

	labels := map[string]string{
		networks.ManagedLabel:     "true",
		networks.ResourceLabel:    networks.ProjectNetworkResource,
		networks.ProjectIDLabel:   "project-1",
		networks.LogicalNameLabel: "frontend",
	}
	frontend := networks.Attachment{LogicalName: "frontend", RuntimeName: "frontend", Driver: "bridge", Managed: true, Labels: labels}
	plan := networks.Plan{Mode: networks.ModeMultiNetwork, Attachments: []networks.Attachment{frontend}}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, plan); err != nil {
		t.Fatalf("ensureDockerManagedNetworks create returned error: %v", err)
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, plan); err != nil {
		t.Fatalf("ensureDockerManagedNetworks reuse returned error: %v", err)
	}
	mu.Lock()
	gotCreateCalls := createCalls
	mu.Unlock()
	if gotCreateCalls != 1 {
		t.Fatalf("network create calls = %d, want 1", gotCreateCalls)
	}

	backend := frontend
	backend.LogicalName = "backend"
	backend.RuntimeName = "backend"
	backend.Aliases = []string{"api"}
	backend.Labels = cloneStringMap(labels)
	backend.Labels[networks.LogicalNameLabel] = "backend"
	mu.Lock()
	inspectByName["backend"] = networkapi.Inspect{Name: "backend", ID: "id-backend", Driver: "bridge", Scope: "local", EnableIPv4: true, Labels: backend.Labels}
	mu.Unlock()
	plan.Attachments = append(plan.Attachments, backend)
	containerInfo := containerapi.InspectResponse{
		ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "container-1"},
		NetworkSettings: &containerapi.NetworkSettings{Networks: map[string]*networkapi.EndpointSettings{
			"frontend": {NetworkID: "id-frontend"},
		}},
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, containerInfo, plan); err != nil {
		t.Fatalf("ensureDockerContainerAttachments returned error: %v", err)
	}
	mu.Lock()
	gotConnectCalls := append([]string(nil), connectCalls...)
	mu.Unlock()
	if len(gotConnectCalls) != 1 || gotConnectCalls[0] != "backend" {
		t.Fatalf("network connect calls = %#v, want backend", gotConnectCalls)
	}

	mu.Lock()
	inspectByName["id-rogue"] = networkapi.Inspect{ID: "id-rogue", Labels: map[string]string{
		networks.ManagedLabel:  "true",
		networks.ResourceLabel: networks.ProjectNetworkResource,
	}}
	mu.Unlock()
	containerInfo.NetworkSettings.Networks["rogue"] = &networkapi.EndpointSettings{NetworkID: "id-rogue"}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, containerInfo, plan); err == nil || !strings.Contains(err.Error(), "unexpected managed project network") {
		t.Fatalf("unexpected managed attachment error = %v", err)
	}
}

func TestDockerManagedNetworkCreateRaceViaAPI(t *testing.T) {
	ctx := context.Background()
	var inspectCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/networks/frontend"):
			inspectCalls++
			if inspectCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "network not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(networkapi.Inspect{
				Name: "frontend", ID: "network-frontend", Driver: "bridge", Scope: "local", EnableIPv4: true,
				Labels: map[string]string{networks.ManagedLabel: "true", networks.ResourceLabel: networks.ProjectNetworkResource},
			})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/networks/create"):
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "network already exists"})
		default:
			t.Errorf("unexpected Docker API request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dockerClient, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.43"))
	if err != nil {
		t.Fatalf("NewClientWithOpts returned error: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	attachment := networks.Attachment{
		RuntimeName: "frontend",
		Managed:     true,
		Labels: map[string]string{
			networks.ManagedLabel:  "true",
			networks.ResourceLabel: networks.ProjectNetworkResource,
		},
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, networks.Plan{Mode: networks.ModeMultiNetwork, Attachments: []networks.Attachment{attachment}}); err != nil {
		t.Fatalf("ensureDockerManagedNetworks race returned error: %v", err)
	}
	if inspectCalls != 2 {
		t.Fatalf("network inspect calls = %d, want 2", inspectCalls)
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, networks.Plan{Mode: networks.ModeSingleBridge}); err != nil {
		t.Fatalf("single-bridge ensure returned error: %v", err)
	}
}

func TestIntegrationDockerNamedNetworkPlanningWorkflow(t *testing.T) {
	TestDockerNetworkPlanKeepsBaselineWithoutIntent(t)
	TestDockerNetworkPlanRejectsAdditionalBridgeForBridgeDaemon(t)
	TestDockerNetworkPlanRejectsRemoteAndUnknownDaemon(t)
	TestMergeDockerNetworkPortConfig(t)
	TestMergeDockerNetworkPortConfigRejectsInvalidCanonicalValues(t)
	TestDockerSandboxNetworkStateIncludesPortOnlyBaseline(t)
	TestDockerContainerNetworkingConfigUsesPrimaryAttachment(t)
	TestValidateDockerManagedNetworkRejectsPropertyAndOwnershipDrift(t)
	TestDockerContainerIDFromMountInfo(t)
	TestDockerManagedNetworkLifecycleViaAPI(t)
	TestDockerManagedNetworkCreateRaceViaAPI(t)
}

func TestE2EDockerNamedNetworkPlanningWorkflow(t *testing.T) {
	TestIntegrationDockerNamedNetworkPlanningWorkflow(t)
}
