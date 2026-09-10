package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCLILLMCommand(cli *cliOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "llm", Short: "Manage daemon LLM configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(newCLILLMProviderCommand(cli))
	return cmd
}

func newCLILLMProviderCommand(cli *cliOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "provider", Short: "Manage API-owned upstream LLM providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	listCmd := &cobra.Command{Use: "ls", Short: "List API-owned upstream LLM providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runComposeLLMProviderListCommand(cmd, *cli)
	}}
	createOptions := composeLLMProviderCreateOptions{}
	createCmd := &cobra.Command{Use: "create <id>", Short: "Create an API-owned upstream LLM provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runComposeLLMProviderCreateCommand(cmd, *cli, createOptions, args[0])
	}}
	addLLMProviderCreateFlags(createCmd, &createOptions)
	inspectCmd := &cobra.Command{Use: "inspect <id>", Short: "Inspect an API-owned upstream LLM provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runComposeLLMProviderInspectCommand(cmd, *cli, args[0])
	}}
	updateOptions := composeLLMProviderUpdateOptions{}
	updateCmd := &cobra.Command{Use: "update <id>", Short: "Update an API-owned upstream LLM provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runComposeLLMProviderUpdateCommand(cmd, *cli, updateOptions, args[0])
	}}
	addLLMProviderUpdateFlags(updateCmd, &updateOptions)
	removeCmd := &cobra.Command{Use: "rm <id>", Aliases: []string{"remove"}, Short: "Remove an API-owned upstream LLM provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runComposeLLMProviderRemoveCommand(cmd, *cli, args[0])
	}}
	cmd.AddCommand(listCmd, createCmd, inspectCmd, updateCmd, removeCmd)
	return cmd
}

func addLLMProviderCreateFlags(cmd *cobra.Command, options *composeLLMProviderCreateOptions) {
	cmd.Flags().StringVar(&options.Name, "name", "", "Display name; defaults to the provider id")
	cmd.Flags().StringVar(&options.BaseURL, "base-url", "", "Absolute HTTP(S) upstream base URL")
	cmd.Flags().StringVar(&options.Protocol, "protocol", "", "Upstream protocol: responses, chat_completions, or anthropic_messages")
	cmd.Flags().StringVar(&options.APIKey, "api-key", "", "Literal upstream API key")
	cmd.Flags().BoolVar(&options.Enabled, "enabled", true, "Whether the provider is enabled")
	for _, name := range []string{"base-url", "protocol", "api-key"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("mark %s required: %v", name, err))
		}
	}
}

func addLLMProviderUpdateFlags(cmd *cobra.Command, options *composeLLMProviderUpdateOptions) {
	cmd.Flags().StringVar(&options.Name, "name", "", "Display name")
	cmd.Flags().StringVar(&options.BaseURL, "base-url", "", "Absolute HTTP(S) upstream base URL")
	cmd.Flags().StringVar(&options.Protocol, "protocol", "", "Upstream protocol: responses, chat_completions, or anthropic_messages")
	cmd.Flags().StringVar(&options.APIKey, "api-key", "", "Literal upstream API key")
	cmd.Flags().BoolVar(&options.Enabled, "enabled", true, "Whether the provider is enabled")
}
