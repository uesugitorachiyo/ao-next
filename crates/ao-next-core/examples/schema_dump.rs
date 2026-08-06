use ao_next_core::adapter::AdapterTurn;
use ao_next_core::contracts::generated_contract_schemas;
use schemars::schema_for;

fn main() {
    for (file_name, schema) in generated_contract_schemas() {
        println!("=== {file_name}");
        println!(
            "{}",
            serde_json::to_string_pretty(&schema).expect("serialize generated schema")
        );
    }
    println!("=== adapter-turn-v1.schema.json");
    println!(
        "{}",
        serde_json::to_string_pretty(&schema_for!(AdapterTurn))
            .expect("serialize adapter-turn schema")
    );
}
