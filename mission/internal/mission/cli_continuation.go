package mission

func registerContinuationCLICommands(registry cliCommandRegistry) {
	for _, command := range []string{
		"status",
		"next",
		"continue",
		"checkpoint",
		"pause",
		"resume",
		"stop",
	} {
		registry[command] = runCLICommand
	}
}
