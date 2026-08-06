package main

import (
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"os"
	"strings"
	"time"
)

type cliClientConfig struct {
	BaseURL       string
	SocketPath    string
	Source        string
	SourceValue   string
	UseUnixSocket bool
	AuthToken     string
	Timeout       time.Duration
}

type cliServiceClients struct {
	project  agentcomposev2connect.ProjectServiceClient
	run      agentcomposev2connect.RunServiceClient
	exec     agentcomposev2connect.ExecServiceClient
	resource agentcomposev2connect.ResourceServiceClient
	image    agentcomposev2connect.ImageServiceClient
	cache    agentcomposev2connect.CacheServiceClient
	volume   agentcomposev2connect.VolumeServiceClient
	sandbox  agentcomposev2connect.SandboxServiceClient

	// attachBidiProbe is a side-effect-free pre-attach h2c capability check,
	// or nil when probing does not apply (unix socket, https). It runs before
	// any AttachAgentRun/AttachExec bidi stream is opened so an HTTP/1-only
	// proxy is reported before resources are prepared.
	attachBidiProbe func(context.Context) error
}

func newCLIServiceClients(cli cliOptions) (cliServiceClients, error) {
	clientConfig, err := resolveCLIServiceClientConfig(cli)
	if err != nil {
		return cliServiceClients{}, err
	}
	httpClient := newDaemonHTTPClient(clientConfig)
	return cliServiceClients{
		project:         agentcomposev2connect.NewProjectServiceClient(httpClient, clientConfig.BaseURL),
		run:             agentcomposev2connect.NewRunServiceClient(httpClient, clientConfig.BaseURL),
		exec:            agentcomposev2connect.NewExecServiceClient(httpClient, clientConfig.BaseURL),
		resource:        agentcomposev2connect.NewResourceServiceClient(httpClient, clientConfig.BaseURL),
		image:           agentcomposev2connect.NewImageServiceClient(httpClient, clientConfig.BaseURL),
		cache:           agentcomposev2connect.NewCacheServiceClient(httpClient, clientConfig.BaseURL),
		volume:          agentcomposev2connect.NewVolumeServiceClient(httpClient, clientConfig.BaseURL),
		sandbox:         agentcomposev2connect.NewSandboxServiceClient(httpClient, clientConfig.BaseURL),
		attachBidiProbe: newAttachBidiProbe(clientConfig),
	}, nil
}

func resolveCLIServiceClientConfig(cli cliOptions) (cliClientConfig, error) {
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return cliClientConfig{}, err
	}
	clientConfig.Timeout = cli.Timeout
	return clientConfig, nil
}

func resolveCLIClientConfig(hostFlag string) (cliClientConfig, error) {
	clientConfig, err := resolveCLIClientEndpoint(hostFlag)
	if err != nil || clientConfig.UseUnixSocket {
		return clientConfig, err
	}
	if err := applyStoredCLIAuth(&clientConfig); err != nil {
		return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
	}
	return clientConfig, nil
}

func resolveCLIClientEndpoint(hostFlag string) (cliClientConfig, error) {
	hostFlag = strings.TrimSpace(hostFlag)
	if hostFlag != "" {
		baseURL, err := normalizeCLIHost("--host", hostFlag)
		if err != nil {
			return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		config := cliClientConfig{
			BaseURL:     baseURL,
			Source:      "--host",
			SourceValue: hostFlag,
		}
		return config, nil
	}

	if envHost := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_HOST")); envHost != "" {
		baseURL, err := normalizeCLIHost("AGENT_COMPOSE_HOST", envHost)
		if err != nil {
			return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		config := cliClientConfig{
			BaseURL:     baseURL,
			Source:      "AGENT_COMPOSE_HOST",
			SourceValue: envHost,
		}
		return config, nil
	}

	socketPath, err := resolveAgentComposeSocketForCLI(os.Getenv("AGENT_COMPOSE_SOCKET"))
	if err != nil {
		return cliClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
	}
	return cliClientConfig{
		BaseURL:       "http://agent-compose",
		SocketPath:    socketPath,
		Source:        "AGENT_COMPOSE_SOCKET",
		SourceValue:   socketPath,
		UseUnixSocket: true,
	}, nil
}
