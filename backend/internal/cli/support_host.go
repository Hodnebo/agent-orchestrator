package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/triage"
)

func runSupportHost(ctx context.Context, in io.Reader, out io.Writer, dataDir string) error {
	return triage.Serve(ctx, in, out, triage.DefaultOptions(dataDir))
}

func newSupportHostCommand(c *commandContext) *cobra.Command {
	var dataDir string
	var tool bool
	var runID string
	cmd := &cobra.Command{
		Use:    "support-host",
		Short:  "Run the independent support workflow (internal)",
		Hidden: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageError{fmt.Errorf("support-host takes no positional arguments")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tool && strings.TrimSpace(runID) == "" || !tool && runID != "" {
				return usageError{fmt.Errorf("support-host --tool requires --run ID; --run is only valid with --tool")}
			}
			dataDir = strings.TrimSpace(dataDir)
			if dataDir == "" {
				paths, err := config.ResolvePaths()
				if paths.DataDir == "" {
					return fmt.Errorf("resolve support data directory: %w", err)
				}
				dataDir = paths.DataDir
			}
			if !filepath.IsAbs(dataDir) {
				return usageError{fmt.Errorf("support-host data directory must be absolute")}
			}
			if tool {
				return triage.ServeTool(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), runID, triage.DefaultOptions(dataDir))
			}
			return c.deps.SupportHost(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), dataDir)
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "AO data directory")
	cmd.Flags().BoolVar(&tool, "tool", false, "Handle one explicit support operation from stdin")
	cmd.Flags().StringVar(&runID, "run", "", "Handed-off report run ID")
	return cmd
}

// Internal support IPC can contain private observations even on parse errors.
func isSupportHostInvocation(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "support-host" {
			return true
		}
	}
	return false
}
