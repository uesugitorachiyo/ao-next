package mission

func registerCorrelationCLICommands(registry cliCommandRegistry) {
	registry["correlation"] = runCLICommand
}
