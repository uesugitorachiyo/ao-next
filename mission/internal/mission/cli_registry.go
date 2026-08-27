package mission

import (
	"errors"
	"fmt"
	"io"
)

const cliUsage = "usage: ao-mission [--home <dir>] <init|start|objective|mission|issue-repair|continue|checkpoint|status|next|stop|pause|resume|doctor|schedule|qualification|daemon|telegram|a2a|gateway|governance|command|artifacts|correlation|validate|import|final|terminal-index>"

type cliCommandHandler func(Store, []string, io.Writer) error

type cliCommandRegistry map[string]cliCommandHandler

func Run(args []string, stdout, stderr io.Writer) int {
	if err := run(args, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "ao-mission version=%s source_sha=%s\n", BuildVersion, BuildSourceSHA)
		return nil
	}
	if len(args) == 0 {
		return errors.New(cliUsage)
	}
	home, args, err := parseGlobalHome(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("command is required")
	}
	handler, ok := newCLICommandRegistry()[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q", args[0])
	}
	return handler(NewStore(home), args, stdout)
}

func newCLICommandRegistry() cliCommandRegistry {
	registry := cliCommandRegistry{}
	registerGeneralCLICommands(registry)
	registerMissionCLICommands(registry)
	registerContinuationCLICommands(registry)
	registerCorrelationCLICommands(registry)
	registerIssueRepairCLICommands(registry)
	registerTerminalIndexCLICommands(registry)
	return registry
}

func registerGeneralCLICommands(registry cliCommandRegistry) {
	for _, command := range []string{
		"init",
		"start",
		"objective",
		"doctor",
		"schedule",
		"qualification",
		"daemon",
		"telegram",
		"a2a",
		"gateway",
		"governance",
		"command",
		"artifacts",
		"validate",
		"import",
		"final",
	} {
		registry[command] = runCLICommand
	}
}
