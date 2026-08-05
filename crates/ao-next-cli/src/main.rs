const fn schema_version() -> &'static str {
    "ao.next.cli.v1"
}

fn main() {
    if std::env::args().nth(1).as_deref() == Some("--schema-version") {
        println!("{}", schema_version());
    } else {
        eprintln!("ao-next: use --schema-version to inspect the CLI contract");
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn cli_exposes_its_schema_version() {
        assert_eq!(super::schema_version(), "ao.next.cli.v1");
    }
}
