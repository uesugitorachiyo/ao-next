package mission

func registerMissionCLICommands(registry cliCommandRegistry) {
	registry["mission"] = runCLICommand
}
