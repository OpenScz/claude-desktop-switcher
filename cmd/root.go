package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/TinoA/claude-desktop-switcher/internal/platform"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"
var dryRun bool

const ProductName = "Claude Desktop Switcher"

var root = &cobra.Command{
	Use:     "claude-desktop-switcher",
	Short:   "Claude Desktop Switcher: switch Claude Desktop accounts without logging out",
	Version: Version,
}

func Execute() {
	root.SetArgs(startupArgs(os.Args[1:], runtime.GOOS))
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func startupArgs(args []string, goos string) []string {
	if len(args) == 0 && goos == "windows" {
		return []string{"tray"}
	}
	return args
}

func init() {
	// Explorer is the parent for a double-click or a desktop shortcut, and cobra's
	// guard for command-line tools would then wait five seconds and exit instead
	// of opening the tray.
	cobra.MousetrapHelpText = ""
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "show paths and actions without changing data")
	root.AddCommand(cmdSave, cmdAdd, cmdUse, cmdList, cmdDelete, cmdStatus, cmdTray, cmdExport, cmdImport)
}

func printDryRun(action string, name string) error {
	p := platform.Current()
	appData, err := p.AppDataPath()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "DRY RUN: %s\napp-data: %s\nCookies: %s\nprofile: %s\nNo data, processes, or sessions will be modified.\n", action, appData, platform.CookiesPath(appData), name)
	return nil
}
