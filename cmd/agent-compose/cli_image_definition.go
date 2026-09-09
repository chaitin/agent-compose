package main

import "github.com/spf13/cobra"

func newCLIImagesCommand(cli *cliOptions) *cobra.Command {
	return newCLIImageListCommand(cli, "images", "List daemon images")
}

func newCLIImageCommand(cli *cliOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use: "image", Short: "Manage daemon images", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newCLIImageListCommand(cli, "ls", "List daemon images", "image ls", "images"),
		newCLIImagePullCommand(cli, "pull [image]", "image pull [image]", "pull [image]"),
		newCLIImageBuildCommand(cli, "build [agent...]", "image build [agent...]", "build [agent...]"),
		newCLIImageRemoveCommand(cli, "rm <image>", "image rm <image>", "rmi <image>"),
		newCLIImageInspectCommandWithWarning(cli, "inspect <image>", "image inspect <image>", "inspect image <image>"),
	)
	return cmd
}

func newCLILegacyImageCommands(cli *cliOptions) []*cobra.Command {
	return []*cobra.Command{
		newCLIImagePullCommand(cli, "pull [image]"),
		newCLIImageBuildCommand(cli, "build [agent...]"),
		newCLIImageRemoveCommand(cli, "rmi <image>"),
	}
}

func newCLIImageListCommand(cli *cliOptions, use, short string, warning ...string) *cobra.Command {
	options := composeImageListOptions{}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if len(warning) == 2 {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), warning[0], warning[1]); err != nil {
				return err
			}
		}
		return runComposeImageListCommand(cmd, *cli, options)
	}}
	addImageListFlags(cmd, &options)
	return cmd
}

func newCLIImagePullCommand(cli *cliOptions, use string, warning ...string) *cobra.Command {
	options := composeImagePullOptions{}
	cmd := &cobra.Command{Use: use, Short: "Pull an image or all project images", Args: cobra.RangeArgs(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(warning) == 2 {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), warning[0], warning[1]); err != nil {
				return err
			}
		}
		return runComposePullCommand(cmd, *cli, options, args)
	}}
	addImagePullFlags(cmd, &options)
	return cmd
}

func newCLIImageBuildCommand(cli *cliOptions, use string, warning ...string) *cobra.Command {
	options := composeImageBuildOptions{}
	cmd := &cobra.Command{Use: use, Short: "Build project agent images", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if len(warning) == 2 {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), warning[0], warning[1]); err != nil {
				return err
			}
		}
		return runComposeBuildCommand(cmd, *cli, options, args)
	}}
	addImageBuildFlags(cmd, &options)
	return cmd
}

func newCLIImageRemoveCommand(cli *cliOptions, use string, warning ...string) *cobra.Command {
	options := composeImageRemoveOptions{}
	cmd := &cobra.Command{Use: use, Short: "Remove an image", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(warning) == 2 {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), warning[0], warning[1]); err != nil {
				return err
			}
		}
		return runComposeImageRemoveCommand(cmd, *cli, options, args[0])
	}}
	addImageRemoveFlags(cmd, &options)
	return cmd
}

func newCLIImageInspectCommand(cli *cliOptions) *cobra.Command {
	return newCLIImageInspectCommandWithWarning(cli, "inspect <image>")
}

func newCLIImageInspectCommandWithWarning(cli *cliOptions, use string, warning ...string) *cobra.Command {
	return &cobra.Command{Use: use, Short: "Inspect an image", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(warning) == 2 {
			if err := writeDeprecatedWarning(cmd.ErrOrStderr(), warning[0], warning[1]); err != nil {
				return err
			}
		}
		return runComposeImageInspectCommand(cmd, *cli, args[0])
	}}
}
