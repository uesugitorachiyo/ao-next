use ao_next_core::contracts::generated_contract_schemas;

fn main() {
    for (file_name, schema) in generated_contract_schemas() {
        println!("=== {file_name}");
        println!(
            "{}",
            serde_json::to_string_pretty(&schema).expect("serialize generated schema")
        );
    }
}
