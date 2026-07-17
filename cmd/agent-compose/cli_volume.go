package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeVolumeListOptions struct {
	Query     string
	Driver    string
	ProjectID string
	Verbose   bool
}

type composeVolumeCreateOptions struct {
	Driver  string
	Labels  []string
	Options []string
}

type composeVolumeRemoveOptions struct {
	Force bool
}

type composeVolumePruneOptions struct {
	composeVolumeListOptions
	Force bool
}

func addVolumeListFlags(cmd *cobra.Command, options *composeVolumeListOptions) {
	cmd.Flags().StringVar(&options.Query, "query", "", "Filter volumes by name or id")
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Filter volumes by driver")
	cmd.Flags().StringVar(&options.ProjectID, "project-id", "", "Filter volumes by project id")
}

func addVolumeCreateFlags(cmd *cobra.Command, options *composeVolumeCreateOptions) {
	cmd.Flags().StringVar(&options.Driver, "driver", "local", "Volume driver")
	cmd.Flags().StringArrayVar(&options.Labels, "label", nil, "Set volume label as key=value")
	cmd.Flags().StringArrayVar(&options.Options, "opt", nil, "Set volume driver option as key=value")
}

func addVolumeRemoveFlags(cmd *cobra.Command, options *composeVolumeRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Force volume removal")
}

func addVolumePruneFlags(cmd *cobra.Command, options *composeVolumePruneOptions) {
	addVolumeListFlags(cmd, &options.composeVolumeListOptions)
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove matched volumes")
}

func runComposeVolumeListCommand(cmd *cobra.Command, cli cliOptions, options composeVolumeListOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.volume.ListVolumes(cmd.Context(), connect.NewRequest(&agentcomposev2.ListVolumesRequest{
		Query:     strings.TrimSpace(options.Query),
		Driver:    strings.TrimSpace(options.Driver),
		ProjectId: strings.TrimSpace(options.ProjectID),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list volumes: %w", err))
	}
	output := composeVolumeListOutputFromResponse(resp.Msg)
	projects, err := listAllProjects(cmd.Context(), clients.project)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list projects for volumes: %w", err))
	}
	setComposeVolumeProjectNames(output.Volumes, projects.Projects)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeVolumesText(cmd.OutOrStdout(), output.Volumes, options.Verbose)
}

func runComposeVolumeCreateCommand(cmd *cobra.Command, cli cliOptions, options composeVolumeCreateOptions, name string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	labels, err := parseCLIStringMap(options.Labels, "--label")
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	driverOptions, err := parseCLIStringMap(options.Options, "--opt")
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.volume.CreateVolume(cmd.Context(), connect.NewRequest(&agentcomposev2.CreateVolumeRequest{
		Name:    strings.TrimSpace(name),
		Driver:  strings.TrimSpace(options.Driver),
		Labels:  labels,
		Options: driverOptions,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("create volume %s: %w", strings.TrimSpace(name), err))
	}
	output := composeVolumeCreateOutput{Volume: composeVolumeOutputFromProto(resp.Msg.GetVolume()), Created: resp.Msg.GetCreated()}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), output.Volume.Name)
	return err
}

func runComposeVolumeInspectCommand(cmd *cobra.Command, cli cliOptions, name string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("volume inspect requires a volume name")}
	}
	resp, err := clients.volume.InspectVolume(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectVolumeRequest{Name: name}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect volume %s: %w", name, err))
	}
	output := composeVolumeInspectOutput{Volume: composeVolumeOutputFromProto(resp.Msg.GetVolume())}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeVolumeInspectText(cmd.OutOrStdout(), output)
}

func runComposeVolumeRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeVolumeRemoveOptions, names []string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeVolumeRemoveOutput{Removed: make([]string, 0, len(names))}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		resp, err := clients.volume.RemoveVolume(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveVolumeRequest{Name: name, Force: options.Force}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("remove volume %s: %w", name, err))
		}
		if resp.Msg.GetRemoved() {
			output.Removed = append(output.Removed, firstNonEmptyString(resp.Msg.GetName(), name))
		}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, name := range output.Removed {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
			return err
		}
	}
	return nil
}

func runComposeVolumePruneCommand(cmd *cobra.Command, cli cliOptions, options composeVolumePruneOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.volume.PruneVolumes(cmd.Context(), connect.NewRequest(&agentcomposev2.PruneVolumesRequest{
		Query:     strings.TrimSpace(options.Query),
		Driver:    strings.TrimSpace(options.Driver),
		ProjectId: strings.TrimSpace(options.ProjectID),
		Force:     options.Force,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("prune volumes: %w", err))
	}
	output := composeVolumePruneOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeVolumePruneOutput(cmd.OutOrStdout(), output)
}

type composeVolumeListOutput struct {
	Volumes []composeVolumeOutput `json:"volumes"`
}

type composeVolumeInspectOutput struct {
	Volume composeVolumeOutput `json:"volume"`
}

type composeVolumeCreateOutput struct {
	Volume  composeVolumeOutput `json:"volume"`
	Created bool                `json:"created"`
}

type composeVolumeRemoveOutput struct {
	Removed []string `json:"removed"`
}

type composeVolumePruneOutput struct {
	DryRun  bool                  `json:"dry_run"`
	Matched []composeVolumeOutput `json:"matched"`
	Removed []composeVolumeOutput `json:"removed"`
	Skipped []composeVolumeOutput `json:"skipped"`
}

type composeVolumeOutput struct {
	Name        string            `json:"name"`
	Driver      string            `json:"driver"`
	Path        string            `json:"path,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	ProjectName string            `json:"project_name,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

func composeVolumeListOutputFromResponse(resp *agentcomposev2.ListVolumesResponse) composeVolumeListOutput {
	output := composeVolumeListOutput{Volumes: make([]composeVolumeOutput, 0, len(resp.GetVolumes()))}
	for _, volume := range resp.GetVolumes() {
		output.Volumes = append(output.Volumes, composeVolumeOutputFromProto(volume))
	}
	return output
}

func setComposeVolumeProjectNames(volumes []composeVolumeOutput, projects []composeProjectListItem) {
	projectNames := make(map[string]string, len(projects))
	for _, project := range projects {
		projectNames[project.ID] = project.Name
	}
	for index := range volumes {
		volumes[index].ProjectName = projectNames[displayOpaqueID(volumes[index].ProjectID)]
	}
}

func composeVolumePruneOutputFromResponse(resp *agentcomposev2.PruneVolumesResponse) composeVolumePruneOutput {
	output := composeVolumePruneOutput{
		DryRun:  resp.GetDryRun(),
		Matched: make([]composeVolumeOutput, 0, len(resp.GetMatched())),
		Removed: make([]composeVolumeOutput, 0, len(resp.GetRemoved())),
		Skipped: make([]composeVolumeOutput, 0, len(resp.GetSkipped())),
	}
	for _, volume := range resp.GetMatched() {
		output.Matched = append(output.Matched, composeVolumeOutputFromProto(volume))
	}
	for _, volume := range resp.GetRemoved() {
		output.Removed = append(output.Removed, composeVolumeOutputFromProto(volume))
	}
	for _, volume := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeVolumeOutputFromProto(volume))
	}
	return output
}

func composeVolumeOutputFromProto(volume *agentcomposev2.Volume) composeVolumeOutput {
	if volume == nil {
		return composeVolumeOutput{}
	}
	return composeVolumeOutput{
		Name:      volume.GetName(),
		Driver:    volume.GetDriver(),
		Path:      volume.GetPath(),
		Labels:    cloneStringMapForCLI(volume.GetLabels()),
		Options:   cloneStringMapForCLI(volume.GetOptions()),
		ProjectID: volume.GetProjectId(),
		CreatedAt: volume.GetCreatedAt(),
		UpdatedAt: volume.GetUpdatedAt(),
	}
}

func writeVolumesText(out io.Writer, volumes []composeVolumeOutput, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "NAME\tDRIVER\tPROJECT\tPATH"
	if verbose {
		header = "NAME\tDRIVER\tPROJECT\tPROJECT ID\tPATH"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for _, volume := range volumes {
		project := firstNonEmptyString(volume.ProjectName, shortOpaqueID(volume.ProjectID), "-")
		var err error
		if verbose {
			_, err = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				firstNonEmptyString(volume.Name, "-"), firstNonEmptyString(volume.Driver, "-"), project,
				firstNonEmptyString(volume.ProjectID, "-"), firstNonEmptyString(volume.Path, "-"))
		} else {
			_, err = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				firstNonEmptyString(volume.Name, "-"), firstNonEmptyString(volume.Driver, "-"), project,
				firstNonEmptyString(volume.Path, "-"))
		}
		if err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeVolumeInspectText(out io.Writer, output composeVolumeInspectOutput) error {
	volume := output.Volume
	if _, err := fmt.Fprintf(out, "Name: %s\nDriver: %s\nPath: %s\nProject: %s\nCreated: %s\nUpdated: %s\n",
		firstNonEmptyString(volume.Name, "-"),
		firstNonEmptyString(volume.Driver, "-"),
		firstNonEmptyString(volume.Path, "-"),
		firstNonEmptyString(volume.ProjectID, "-"),
		firstNonEmptyString(volume.CreatedAt, "-"),
		firstNonEmptyString(volume.UpdatedAt, "-"),
	); err != nil {
		return err
	}
	if err := writeStringMapSection(out, "Labels", volume.Labels); err != nil {
		return err
	}
	return writeStringMapSection(out, "Options", volume.Options)
}

func writeVolumePruneOutput(out io.Writer, output composeVolumePruneOutput) error {
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d matched, %d skipped, %d would be removed.\n", len(output.Matched), len(output.Skipped), len(output.Matched)); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "Removed %d volume(s); %d matched, %d skipped.\n", len(output.Removed), len(output.Matched), len(output.Skipped)); err != nil {
			return err
		}
	}
	if len(output.Removed) > 0 {
		if _, err := fmt.Fprintln(out, "Removed:"); err != nil {
			return err
		}
		if err := writeVolumeOperationTable(out, output.Removed); err != nil {
			return err
		}
	}
	if len(output.Matched) > 0 {
		if _, err := fmt.Fprintln(out, "Matched:"); err != nil {
			return err
		}
		if err := writeVolumeOperationTable(out, output.Matched); err != nil {
			return err
		}
	}
	if len(output.Skipped) > 0 {
		if _, err := fmt.Fprintln(out, "Skipped:"); err != nil {
			return err
		}
		if err := writeVolumeOperationTable(out, output.Skipped); err != nil {
			return err
		}
	}
	return nil
}

func writeVolumeOperationTable(out io.Writer, volumes []composeVolumeOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tDRIVER\tPROJECT\tPATH"); err != nil {
		return err
	}
	for _, volume := range volumes {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			firstNonEmptyString(volume.Name, "-"),
			firstNonEmptyString(volume.Driver, "-"),
			firstNonEmptyString(volume.ProjectID, "-"),
			firstNonEmptyString(volume.Path, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}
