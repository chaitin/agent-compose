package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"agent-compose/pkg/clientconfig"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"connectrpc.com/connect"
)

type cliAuthLoginOptions struct {
	Token string
}

func newCLIAuthCommand(options *cliOptions) *cobra.Command {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage daemon authentication",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	loginOptions := cliAuthLoginOptions{}
	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Verify and save a daemon token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCLIAuthLogin(cmd, options.Host, loginOptions)
		},
	}
	loginCmd.Flags().StringVar(&loginOptions.Token, "token", "", "Daemon bearer token")
	if err := loginCmd.MarkFlagRequired("token"); err != nil {
		panic(err)
	}
	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove a saved daemon token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCLIAuthLogout(cmd, options.Host)
		},
	}
	listCmd := &cobra.Command{
		Use:   "ls",
		Short: "List authenticated daemon sites",
		Args:  cobra.NoArgs,
		RunE:  runCLIAuthList,
	}
	whoAmICmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the current daemon token identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCLIAuthWhoAmI(cmd, *options)
		},
	}
	authCmd.AddCommand(loginCmd, logoutCmd, listCmd, whoAmICmd, newCLIAuthRoleCommand(options), newCLIAuthTokenCommand(options))
	return authCmd
}

func runCLIAuthLogin(cmd *cobra.Command, hostFlag string, options cliAuthLoginOptions) error {
	token := strings.TrimSpace(options.Token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("token must be a non-empty value without whitespace")}
	}
	clientConfig, err := resolveCLIClientEndpoint(hostFlag)
	if err != nil {
		return err
	}
	if clientConfig.UseUnixSocket {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("auth login requires --host or AGENT_COMPOSE_HOST")}
	}
	clientConfig.AuthToken = token
	client := agentcomposev2connect.NewAuthServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	identity, err := client.WhoAmI(cmd.Context(), connect.NewRequest(&agentcomposev2.WhoAmIRequest{}))
	if err != nil {
		var statusErr daemonHTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("authentication failed for %s: token was rejected (HTTP %d)", clientConfig.BaseURL, statusErr.StatusCode)
		}
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			return fmt.Errorf("authentication failed for %s: token was rejected", clientConfig.BaseURL)
		}
		return fmt.Errorf("authenticate daemon %s: %w", clientConfig.BaseURL, err)
	}
	path, err := clientconfig.DefaultPath()
	if err != nil {
		return err
	}
	if err := clientconfig.SaveToken(path, clientConfig.BaseURL, token); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Authenticated %s as %s (%s)\n", clientConfig.BaseURL, identity.Msg.GetToken().GetName(), identity.Msg.GetRole())
	return err
}

func runCLIAuthWhoAmI(cmd *cobra.Command, options cliOptions) error {
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.WhoAmI(cmd.Context(), connect.NewRequest(&agentcomposev2.WhoAmIRequest{}))
	if err != nil {
		return fmt.Errorf("query daemon identity: %w", err)
	}
	if options.JSON {
		return writeProtoJSON(cmd.OutOrStdout(), response.Msg)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "TOKEN\tROLE\tORIGIN\n%s\t%s\t%s\n", firstNonEmptyString(response.Msg.GetToken().GetName(), "-"), response.Msg.GetRole(), response.Msg.GetOrigin())
	return err
}

func runCLIAuthLogout(cmd *cobra.Command, hostFlag string) error {
	clientConfig, err := resolveCLIClientEndpoint(hostFlag)
	if err != nil {
		return err
	}
	if clientConfig.UseUnixSocket {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("auth logout requires --host or AGENT_COMPOSE_HOST")}
	}
	path, err := clientconfig.DefaultPath()
	if err != nil {
		return err
	}
	removed, err := clientconfig.RemoveToken(path, clientConfig.BaseURL)
	if err != nil {
		return err
	}
	if !removed {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("no saved token for %s", clientConfig.BaseURL)}
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Logged out %s\n", clientConfig.BaseURL)
	return err
}

func runCLIAuthList(cmd *cobra.Command, _ []string) error {
	path, err := clientconfig.DefaultPath()
	if err != nil {
		return err
	}
	hosts, err := clientconfig.Hosts(path)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No authenticated Agent-Compose sites.")
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Authenticated Agent-Compose sites:"); err != nil {
		return err
	}
	for _, host := range hosts {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", host); err != nil {
			return err
		}
	}
	return nil
}

func applyStoredCLIAuth(config *cliClientConfig) error {
	path, err := clientconfig.DefaultPath()
	if err != nil {
		return err
	}
	token, err := clientconfig.Token(path, config.BaseURL)
	if err != nil {
		return err
	}
	config.AuthToken = token
	return nil
}

type bearerAuthRoundTripper struct {
	token string
	next  http.RoundTripper
}

func (t bearerAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", bearerScheme+t.token)
	return t.next.RoundTrip(cloned)
}
