pub mod comparison;
pub mod corpus;
pub mod metrics;

/// Returns the schema family implemented by the evaluation crate.
#[must_use]
pub const fn schema_version() -> &'static str {
    "ao.next.eval.v1"
}

#[cfg(test)]
mod tests {
    #[test]
    fn evaluator_exposes_its_schema_version() {
        assert_eq!(crate::schema_version(), "ao.next.eval.v1");
    }
}
