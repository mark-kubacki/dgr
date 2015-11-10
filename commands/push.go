package commands

import (
	"github.com/blablacar/cnt/builder"
	"github.com/spf13/cobra"
)

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "push image(s)",
	Long:  `push images to repository`,
	Run: func(cmd *cobra.Command, args []string) {
		runCleanIfRequested(".", buildArgs)
		discoverAndRunPushType(".", buildArgs)
	},
}

func discoverAndRunPushType(path string, args builder.BuildArgs) {
	if cnt, err := builder.NewAci(path, args); err == nil {
		cnt.Push()
	} else if pod, err := builder.NewPod(path, args); err == nil {
		pod.Push()
	} else {
		panic("Cannot find cnt-manifest.yml")
	}
}

func init() {
	pushCmd.Flags().BoolVarP(&buildArgs.NoTestFail, "no-test-fail", "T", false, "Fail if no tests found")
	pushCmd.Flags().BoolVarP(&buildArgs.Test, "test", "t", false, "Run tests before push")
}
