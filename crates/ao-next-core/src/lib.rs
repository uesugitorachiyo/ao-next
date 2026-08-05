pub mod contracts;
pub mod strict_json;

/// Returns the schema family implemented by the core crate.
#[must_use]
pub const fn schema_version() -> &'static str {
    "ao.next.core.v1"
}

#[cfg(test)]
mod tests {
    #[test]
    fn core_exposes_its_schema_version() {
        assert_eq!(crate::schema_version(), "ao.next.core.v1");
    }
}
