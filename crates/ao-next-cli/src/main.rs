mod commands;

use std::io::Write as _;
use std::process::ExitCode;

use clap::{CommandFactory, Parser, error::ErrorKind};
use commands::{Cli, CommandFailure, CommandOutput};
use serde::Serialize;

#[derive(Serialize)]
struct CliError<'a> {
    schema_version: &'static str,
    code: &'a str,
    message: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    diagnostic: Option<&'a serde_json::Value>,
}

fn main() -> ExitCode {
    match Cli::try_parse() {
        Ok(cli) => finish(commands::execute(cli)),
        Err(error)
            if matches!(
                error.kind(),
                ErrorKind::DisplayHelp | ErrorKind::DisplayVersion
            ) =>
        {
            eprint!("{error}");
            let output = CommandOutput::new(
                serde_json::json!({
                    "schema_version": "ao.next.cli-help.v1",
                    "command": Cli::command().get_name()
                }),
                "displayed command help",
                0,
            );
            finish(Ok(output))
        }
        Err(error) => {
            let message = error.to_string();
            finish(Err(CommandFailure::usage(message)))
        }
    }
}

fn finish(result: Result<CommandOutput, CommandFailure>) -> ExitCode {
    match result {
        Ok(output) => {
            write_machine_json(&output.value);
            eprintln!("ao-next: {}", output.summary);
            ExitCode::from(output.status)
        }
        Err(error) => {
            write_machine_json(&CliError {
                schema_version: "ao.next.cli-error.v1",
                code: error.code,
                message: &error.message,
                diagnostic: error.diagnostic.as_ref(),
            });
            eprintln!("ao-next: {}", error.message);
            ExitCode::from(error.status)
        }
    }
}

fn write_machine_json(value: &impl Serialize) {
    let stdout = std::io::stdout();
    let mut lock = stdout.lock();
    if serde_json::to_writer(&mut lock, value).is_err() || lock.write_all(b"\n").is_err() {
        eprintln!("ao-next: failed to write machine output");
    }
}
