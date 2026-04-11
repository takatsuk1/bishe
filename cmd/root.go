package cmd

import (
	"github.com/spf13/cobra"
)

var (
	rootCmd = &cobra.Command{
		Use:          "go-manus",
		Short:        "go-manus",
		Long:         "go manus command",
		SilenceUsage: true,
	}
)

func init() {
	rootCmd.AddCommand(urlReaderCmd)
	rootCmd.AddCommand(lbsHelperCmd)
	rootCmd.AddCommand(deepResearchCmd)
	rootCmd.AddCommand(scheduleHelperCmd)
	rootCmd.AddCommand(financeHelperCmd)
	rootCmd.AddCommand(resumeCustomizerCmd)
	rootCmd.AddCommand(interviewSimulatorCmd)
	rootCmd.AddCommand(careerRadarCmd)
	rootCmd.AddCommand(hostCmd)
	rootCmd.AddCommand(openaiConnectorCmd)
	rootCmd.AddCommand(allinoneCmd)
}

func Execute() {
	_ = rootCmd.Execute()
}
