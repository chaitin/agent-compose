package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type cliTokenCreateOptions struct {
	Name        string
	Description string
	Role        string
}

type cliAuditListOptions struct {
	Token        string
	Action       string
	ResourceType string
	ResourceID   string
	ProjectID    string
	Status       string
	Since        string
	Until        string
	Limit        uint32
	Cursor       string
}

func newCLIAuthRoleCommand(options *cliOptions) *cobra.Command {
	roleCmd := &cobra.Command{Use: "role", Short: "Inspect daemon roles", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	roleCmd.AddCommand(&cobra.Command{
		Use: "ls", Short: "List daemon roles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runCLIAuthRoleList(cmd, *options) },
	})
	return roleCmd
}

func newCLIAuthTokenCommand(options *cliOptions) *cobra.Command {
	tokenCmd := &cobra.Command{Use: "token", Short: "Manage daemon tokens", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	includeRevoked := false
	limit := uint32(0)
	cursor := ""
	listCmd := &cobra.Command{
		Use: "ls", Short: "List daemon tokens", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCLIAuthTokenList(cmd, *options, includeRevoked, limit, cursor)
		},
	}
	listCmd.Flags().BoolVar(&includeRevoked, "all", false, "Include revoked tokens")
	listCmd.Flags().Uint32Var(&limit, "limit", 0, "Maximum tokens to return")
	listCmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a token cursor")

	createOptions := cliTokenCreateOptions{}
	createCmd := &cobra.Command{
		Use: "create", Short: "Create a client-generated daemon token", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runCLIAuthTokenCreate(cmd, *options, createOptions) },
	}
	createCmd.Flags().StringVar(&createOptions.Name, "name", "", "Unique token name")
	createCmd.Flags().StringVar(&createOptions.Description, "description", "", "Token description")
	createCmd.Flags().StringVar(&createOptions.Role, "role", "", "Token role: admin or read-only-admin")
	_ = createCmd.MarkFlagRequired("name")
	_ = createCmd.MarkFlagRequired("role")

	revokeCmd := &cobra.Command{
		Use: "revoke <token>", Short: "Revoke a daemon token by id or name", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runCLIAuthTokenRevoke(cmd, *options, args[0]) },
	}
	tokenCmd.AddCommand(listCmd, createCmd, revokeCmd)
	return tokenCmd
}

func newCLIAuditCommand(options *cliOptions) *cobra.Command {
	auditCmd := &cobra.Command{Use: "audit", Short: "Inspect daemon operation audits", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	listOptions := cliAuditListOptions{}
	listCmd := &cobra.Command{
		Use: "ls", Short: "List daemon operation audits", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runCLIAuditList(cmd, *options, listOptions) },
	}
	listCmd.Flags().StringVar(&listOptions.Token, "token", "", "Filter by token id or name")
	listCmd.Flags().StringVar(&listOptions.Action, "action", "", "Filter by action")
	listCmd.Flags().StringVar(&listOptions.ResourceType, "resource-type", "", "Filter by resource type")
	listCmd.Flags().StringVar(&listOptions.ResourceID, "resource", "", "Filter by resource id")
	listCmd.Flags().StringVar(&listOptions.ProjectID, "project", "", "Filter by project id")
	listCmd.Flags().StringVar(&listOptions.Status, "status", "", "Filter by status")
	listCmd.Flags().StringVar(&listOptions.Since, "since", "", "Filter audits starting at RFC3339 time")
	listCmd.Flags().StringVar(&listOptions.Until, "until", "", "Filter audits ending at RFC3339 time")
	listCmd.Flags().Uint32Var(&listOptions.Limit, "limit", 0, "Maximum audits to return")
	listCmd.Flags().StringVar(&listOptions.Cursor, "cursor", "", "Continue from an audit cursor")
	auditCmd.AddCommand(listCmd)
	return auditCmd
}

func runCLIAuthRoleList(cmd *cobra.Command, options cliOptions) error {
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.ListRoles(cmd.Context(), connect.NewRequest(&agentcomposev2.ListRolesRequest{}))
	if err != nil {
		return fmt.Errorf("list daemon roles: %w", err)
	}
	if options.JSON {
		return writeProtoJSON(cmd.OutOrStdout(), response.Msg)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ROLE\tREAD ONLY\tDESCRIPTION")
	for _, item := range response.Msg.GetRoles() {
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\n", item.GetName(), item.GetReadOnly(), item.GetDescription())
	}
	return tw.Flush()
}

func runCLIAuthTokenList(cmd *cobra.Command, options cliOptions, includeRevoked bool, limit uint32, cursor string) error {
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.ListTokens(cmd.Context(), connect.NewRequest(&agentcomposev2.ListTokensRequest{IncludeRevoked: includeRevoked, Limit: limit, Cursor: cursor}))
	if err != nil {
		return fmt.Errorf("list daemon tokens: %w", err)
	}
	if options.JSON {
		return writeProtoJSON(cmd.OutOrStdout(), response.Msg)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tROLE\tORIGIN\tSTATUS\tCREATED")
	for _, item := range response.Msg.GetTokens() {
		status := "active"
		if item.GetRevokedAt() != nil {
			status = "revoked"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", shortDisplayID(item.GetId()), item.GetName(), item.GetRole(), item.GetOrigin(), status, formatProtoTime(item.GetCreatedAt()))
	}
	return tw.Flush()
}

func runCLIAuthTokenCreate(cmd *cobra.Command, options cliOptions, createOptions cliTokenCreateOptions) error {
	secret, err := generateClientToken()
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.CreateToken(cmd.Context(), connect.NewRequest(&agentcomposev2.CreateTokenRequest{
		Name: createOptions.Name, Description: createOptions.Description, Role: createOptions.Role,
		Token: secret, ClientRequestId: uuid.NewString(),
	}))
	if err != nil {
		return fmt.Errorf("create daemon token: %w", err)
	}
	if options.JSON {
		output := struct {
			Token string `json:"token"`
			Item  struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Role   string `json:"role"`
				Origin string `json:"origin"`
			} `json:"item"`
			Created bool `json:"created"`
		}{Token: secret, Created: response.Msg.GetCreated()}
		output.Item.ID, output.Item.Name, output.Item.Role, output.Item.Origin = response.Msg.GetItem().GetId(), response.Msg.GetItem().GetName(), response.Msg.GetItem().GetRole(), response.Msg.GetItem().GetOrigin()
		data, marshalErr := json.MarshalIndent(output, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Token created. This secret is shown once; store it securely.\nName: %s\nRole: %s\nToken: %s\n", response.Msg.GetItem().GetName(), response.Msg.GetItem().GetRole(), secret)
	return err
}

func runCLIAuthTokenRevoke(cmd *cobra.Command, options cliOptions, ref string) error {
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.RevokeToken(cmd.Context(), connect.NewRequest(&agentcomposev2.RevokeTokenRequest{Token: ref}))
	if err != nil {
		return fmt.Errorf("revoke daemon token: %w", err)
	}
	if options.JSON {
		return writeProtoJSON(cmd.OutOrStdout(), response.Msg)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s (%s)\n", response.Msg.GetItem().GetName(), shortDisplayID(response.Msg.GetItem().GetId()))
	return err
}

func runCLIAuditList(cmd *cobra.Command, options cliOptions, listOptions cliAuditListOptions) error {
	request := &agentcomposev2.ListOperationAuditsRequest{Token: listOptions.Token, Action: listOptions.Action, ResourceType: listOptions.ResourceType, ResourceId: listOptions.ResourceID, ProjectId: listOptions.ProjectID, Status: listOptions.Status, Limit: listOptions.Limit, Cursor: listOptions.Cursor}
	var err error
	if listOptions.Since != "" {
		request.StartedAfter, err = parseCLITimestamp(listOptions.Since)
		if err != nil {
			return err
		}
	}
	if listOptions.Until != "" {
		request.StartedBefore, err = parseCLITimestamp(listOptions.Until)
		if err != nil {
			return err
		}
	}
	clients, err := newCLIServiceClients(options)
	if err != nil {
		return err
	}
	response, err := clients.auth.ListOperationAudits(cmd.Context(), connect.NewRequest(request))
	if err != nil {
		return fmt.Errorf("list operation audits: %w", err)
	}
	if options.JSON {
		return writeProtoJSON(cmd.OutOrStdout(), response.Msg)
	}
	return writeAuditTable(cmd.OutOrStdout(), response.Msg.GetAudits())
}

func writeAuditTable(out io.Writer, items []*agentcomposev2.OperationAudit) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tTOKEN\tACTION\tOBJECT\tSTATUS")
	for _, item := range items {
		object := item.GetResourceType()
		if item.GetResourceId() != "" {
			object += "/" + item.GetResourceId()
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", formatProtoTime(item.GetStartedAt()), firstNonEmptyString(item.GetTokenName(), item.GetOrigin()), item.GetAction(), object, item.GetStatus())
	}
	return tw.Flush()
}

func generateClientToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return "ac_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func writeProtoJSON(out io.Writer, message proto.Message) error {
	data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(message)
	if err != nil {
		return err
	}
	return writeCommandOutput(out, append(data, '\n'))
}

func parseCLITimestamp(value string) (*timestamppb.Timestamp, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("invalid RFC3339 time %q: %w", value, err)}
	}
	return timestamppb.New(parsed), nil
}

func formatProtoTime(value *timestamppb.Timestamp) string {
	if value == nil {
		return "-"
	}
	return value.AsTime().Local().Format("2006-01-02 15:04:05")
}

func shortDisplayID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
