package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type composeLLMProviderCreateOptions struct {
	Name     string
	BaseURL  string
	Protocol string
	APIKey   string
	Enabled  bool
}

type composeLLMProviderUpdateOptions struct {
	Name     string
	BaseURL  string
	Protocol string
	APIKey   string
	Enabled  bool
}

type composeLLMProviderListOutput struct {
	Providers []composeLLMProviderOutput `json:"providers"`
	Total     uint32                     `json:"total"`
}

type composeLLMProviderInspectOutput struct {
	Provider composeLLMProviderOutput `json:"provider"`
}

type composeLLMProviderCreateOutput struct {
	Provider composeLLMProviderOutput `json:"provider"`
}

type composeLLMProviderUpdateOutput struct {
	Provider composeLLMProviderOutput `json:"provider"`
}

type composeLLMProviderRemoveOutput struct {
	ID      string `json:"id"`
	Removed bool   `json:"removed"`
}

type composeLLMProviderOutput struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Protocol  string `json:"protocol"`
	Enabled   bool   `json:"enabled"`
	APIKeySet bool   `json:"api_key_set"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func runComposeLLMProviderListCommand(cmd *cobra.Command, cli cliOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	request := &agentcomposev2.ListProvidersRequest{Limit: 500}
	response := &agentcomposev2.ListProvidersResponse{}
	for uint32(len(response.Providers)) < response.GetTotal() || response.GetTotal() == 0 {
		request.Offset = uint32(len(response.Providers))
		resp, err := clients.llm.ListProviders(cmd.Context(), connect.NewRequest(request))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list llm providers: %w", err))
		}
		response.Total = resp.Msg.GetTotal()
		response.Providers = append(response.Providers, resp.Msg.GetProviders()...)
		if uint32(len(response.Providers)) >= response.Total {
			break
		}
		if len(resp.Msg.GetProviders()) == 0 {
			return fmt.Errorf("llm provider list pagination did not advance")
		}
	}
	output := composeLLMProviderListOutputFromResponse(response)
	if cli.JSON {
		return writeLLMProviderJSON(cmd.OutOrStdout(), output)
	}
	return writeLLMProviderListText(cmd.OutOrStdout(), output.Providers)
}

func runComposeLLMProviderCreateCommand(cmd *cobra.Command, cli cliOptions, options composeLLMProviderCreateOptions, id string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("llm provider create requires a provider id")}
	}
	spec := &agentcomposev2.LLMProviderSpec{
		Id:       id,
		Name:     strings.TrimSpace(options.Name),
		BaseUrl:  strings.TrimSpace(options.BaseURL),
		Protocol: strings.TrimSpace(options.Protocol),
		ApiKey:   proto.String(options.APIKey),
		Enabled:  proto.Bool(options.Enabled),
	}
	resp, err := clients.llm.CreateProvider(cmd.Context(), connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("create llm provider %s: %w", id, err))
	}
	output := composeLLMProviderCreateOutput{Provider: composeLLMProviderOutputFromProto(resp.Msg.GetProvider())}
	if cli.JSON {
		return writeLLMProviderJSON(cmd.OutOrStdout(), output)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), output.Provider.ID)
	return err
}

func runComposeLLMProviderInspectCommand(cmd *cobra.Command, cli cliOptions, id string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("llm provider inspect requires a provider id")}
	}
	resp, err := clients.llm.GetProvider(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: id}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect llm provider %s: %w", id, err))
	}
	output := composeLLMProviderInspectOutput{Provider: composeLLMProviderOutputFromProto(resp.Msg.GetProvider())}
	if cli.JSON {
		return writeLLMProviderJSON(cmd.OutOrStdout(), output)
	}
	return writeLLMProviderInspectText(cmd.OutOrStdout(), output.Provider)
}

func runComposeLLMProviderUpdateCommand(cmd *cobra.Command, cli cliOptions, options composeLLMProviderUpdateOptions, id string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("llm provider update requires a provider id")}
	}
	spec := &agentcomposev2.LLMProviderSpec{Id: id}
	if cmd.Flags().Changed("name") {
		spec.Name = strings.TrimSpace(options.Name)
	}
	if cmd.Flags().Changed("base-url") {
		spec.BaseUrl = strings.TrimSpace(options.BaseURL)
	}
	if cmd.Flags().Changed("protocol") {
		spec.Protocol = strings.TrimSpace(options.Protocol)
	}
	if cmd.Flags().Changed("api-key") {
		spec.ApiKey = proto.String(options.APIKey)
	}
	if cmd.Flags().Changed("enabled") {
		spec.Enabled = proto.Bool(options.Enabled)
	}
	resp, err := clients.llm.UpdateProvider(cmd.Context(), connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("update llm provider %s: %w", id, err))
	}
	output := composeLLMProviderUpdateOutput{Provider: composeLLMProviderOutputFromProto(resp.Msg.GetProvider())}
	if cli.JSON {
		return writeLLMProviderJSON(cmd.OutOrStdout(), output)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), output.Provider.ID)
	return err
}

func runComposeLLMProviderRemoveCommand(cmd *cobra.Command, cli cliOptions, id string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("llm provider rm requires a provider id")}
	}
	if _, err := clients.llm.DeleteProvider(cmd.Context(), connect.NewRequest(&agentcomposev2.DeleteProviderRequest{Id: id})); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("remove llm provider %s: %w", id, err))
	}
	output := composeLLMProviderRemoveOutput{ID: id, Removed: true}
	if cli.JSON {
		return writeLLMProviderJSON(cmd.OutOrStdout(), output)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), id)
	return err
}

func composeLLMProviderListOutputFromResponse(resp *agentcomposev2.ListProvidersResponse) composeLLMProviderListOutput {
	output := composeLLMProviderListOutput{Providers: make([]composeLLMProviderOutput, 0, len(resp.GetProviders())), Total: resp.GetTotal()}
	for _, provider := range resp.GetProviders() {
		output.Providers = append(output.Providers, composeLLMProviderOutputFromProto(provider))
	}
	return output
}

func composeLLMProviderOutputFromProto(provider *agentcomposev2.LLMProvider) composeLLMProviderOutput {
	if provider == nil {
		return composeLLMProviderOutput{}
	}
	return composeLLMProviderOutput{
		ID:        provider.GetId(),
		Name:      provider.GetName(),
		BaseURL:   provider.GetBaseUrl(),
		Protocol:  provider.GetProtocol(),
		Enabled:   provider.GetEnabled(),
		APIKeySet: provider.GetApiKeySet(),
		CreatedAt: formatProtoTimestamp(provider.GetCreatedAt()),
		UpdatedAt: formatProtoTimestamp(provider.GetUpdatedAt()),
	}
}

func writeLLMProviderJSON(out io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(out, append(data, '\n'))
}

func writeLLMProviderListText(out io.Writer, providers []composeLLMProviderOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tNAME\tPROTOCOL\tENABLED\tBASE URL"); err != nil {
		return err
	}
	for _, provider := range providers {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n",
			firstNonEmptyString(provider.ID, "-"),
			firstNonEmptyString(provider.Name, "-"),
			firstNonEmptyString(provider.Protocol, "-"),
			provider.Enabled,
			firstNonEmptyString(provider.BaseURL, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeLLMProviderInspectText(out io.Writer, provider composeLLMProviderOutput) error {
	_, err := fmt.Fprintf(out, "ID: %s\nName: %s\nBase URL: %s\nProtocol: %s\nEnabled: %t\nAPI Key Set: %t\nCreated: %s\nUpdated: %s\n",
		firstNonEmptyString(provider.ID, "-"),
		firstNonEmptyString(provider.Name, "-"),
		firstNonEmptyString(provider.BaseURL, "-"),
		firstNonEmptyString(provider.Protocol, "-"),
		provider.Enabled,
		provider.APIKeySet,
		firstNonEmptyString(provider.CreatedAt, "-"),
		firstNonEmptyString(provider.UpdatedAt, "-"),
	)
	return err
}
