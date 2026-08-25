package mission

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
)

func registerTerminalIndexCLICommands(registry cliCommandRegistry) {
	registry["terminal-index"] = runTerminalIndexCLI
}

func runTerminalIndexCLI(_ Store, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("terminal-index requires import, inspect, checkpoint, event-index, command-readback, or historical")
	}
	switch args[1] {
	case "import":
		fs := flag.NewFlagSet("terminal-index import", flag.ContinueOnError)
		root := fs.String("root", "", "")
		index := fs.String("index", "", "")
		state := fs.String("state", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" || strings.TrimSpace(*index) == "" || strings.TrimSpace(*state) == "" {
			return errors.New("terminal-index import requires --root, --index, and --state")
		}
		readback, err := ImportTerminalIndex(*root, *index, *state)
		if err != nil {
			return err
		}
		return printJSON(stdout, readback)
	case "inspect", "checkpoint", "event-index", "command-readback":
		fs := flag.NewFlagSet("terminal-index "+args[1], flag.ContinueOnError)
		state := fs.String("state", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if strings.TrimSpace(*state) == "" {
			return errors.New("terminal-index " + args[1] + " requires --state")
		}
		readback, err := LoadTerminalIndexImport(*state)
		if err != nil {
			return err
		}
		readback.Surface = args[1]
		signTerminalIndexImport(&readback)
		return printJSON(stdout, readback)
	case "historical":
		fs := flag.NewFlagSet("terminal-index historical", flag.ContinueOnError)
		root := fs.String("root", "", "")
		generatedAt := fs.String("generated-at", "", "")
		out := fs.String("out", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" || strings.TrimSpace(*generatedAt) == "" || strings.TrimSpace(*out) == "" {
			return errors.New("terminal-index historical requires --root, --generated-at, and --out")
		}
		index, err := BuildHistoricalMissionTerminalIndex(*root, *generatedAt)
		if err != nil {
			return err
		}
		body, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
			return err
		}
		return printJSON(stdout, index)
	default:
		return errors.New("unknown terminal-index subcommand " + args[1])
	}
}
