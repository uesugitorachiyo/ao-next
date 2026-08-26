pub mod adapter;
pub mod capture;
pub mod contracts;
pub mod effects;
pub mod engine;
pub mod evidence;
pub mod mission;
pub mod mission_exchange;
pub mod policy;
pub mod recovery;
pub mod strict_json;
pub mod terminal;
pub mod verifier;

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
