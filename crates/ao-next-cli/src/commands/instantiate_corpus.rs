use std::collections::BTreeMap;

use ao_next_core::contracts::Digest;
use ao_next_core::strict_json::canonical_digest;
use ao_next_eval::corpus::{CorpusManifest, VariantProfile};
use ao_next_eval::metrics::ExecutionVariant;
use serde::{Deserialize, Serialize};

use super::{CommandFailure, CommandOutput, InstantiateCorpusArgs, decode_file};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct LiveCorpusBindings {
    schema_version: String,
    model_identifier: String,
    reasoning_effort: String,
    variants: Vec<LiveVariantBinding>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct LiveVariantBinding {
    variant: ExecutionVariant,
    runtime: String,
    runtime_digest: Digest,
    adapter_version: String,
    adapter_digest: Digest,
}

#[derive(Serialize)]
#[serde(deny_unknown_fields)]
struct CorpusInstantiationRecord {
    schema_version: &'static str,
    parent_corpus_digest: Digest,
    model_identifier: String,
    reasoning_effort: String,
    variants: Vec<LiveVariantBinding>,
    corpus: CorpusManifest,
}

pub fn execute(args: &InstantiateCorpusArgs) -> Result<CommandOutput, CommandFailure> {
    let mut corpus: CorpusManifest = decode_file(&args.corpus)?;
    corpus
        .validate_live()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let bindings: LiveCorpusBindings = decode_file(&args.bindings)?;
    if bindings.schema_version != "ao.next.live-corpus-bindings.v1"
        || bindings.model_identifier.trim().is_empty()
        || !matches!(
            bindings.reasoning_effort.as_str(),
            "low" | "medium" | "high" | "xhigh"
        )
    {
        return Err(CommandFailure::invalid_input(
            "live corpus binding identity is invalid",
        ));
    }
    let by_variant = bindings
        .variants
        .iter()
        .map(|binding| (binding.variant, binding))
        .collect::<BTreeMap<_, _>>();
    if by_variant.len() != 3
        || bindings.variants.len() != 3
        || ![
            ExecutionVariant::N0,
            ExecutionVariant::N4,
            ExecutionVariant::N7,
        ]
        .into_iter()
        .all(|variant| by_variant.contains_key(&variant))
    {
        return Err(CommandFailure::invalid_input(
            "live corpus bindings must contain N0, N4, and N7 exactly once",
        ));
    }
    let model_digest = canonical_digest(&(
        bindings.model_identifier.as_str(),
        bindings.reasoning_effort.as_str(),
    ))
    .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    for task in &mut corpus.tasks {
        for profile in &mut task.variant_profiles {
            apply_binding(
                profile,
                by_variant[&profile.variant],
                &bindings.model_identifier,
                &model_digest,
            )?;
        }
    }
    let parent_corpus_digest = corpus.corpus_digest.clone();
    corpus.corpus_digest = corpus
        .calculated_digest()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    corpus
        .validate_live()
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    let record = CorpusInstantiationRecord {
        schema_version: "ao.next.live-corpus-instantiation.v1",
        parent_corpus_digest,
        model_identifier: bindings.model_identifier,
        reasoning_effort: bindings.reasoning_effort,
        variants: bindings.variants,
        corpus,
    };
    let value = serde_json::to_value(record)
        .map_err(|error| CommandFailure::invalid_input(error.to_string()))?;
    Ok(CommandOutput::new(
        value,
        "instantiated exact-model sealed live corpus",
        0,
    ))
}

fn apply_binding(
    profile: &mut VariantProfile,
    binding: &LiveVariantBinding,
    model_identifier: &str,
    model_digest: &Digest,
) -> Result<(), CommandFailure> {
    if binding.runtime.trim().is_empty()
        || binding.adapter_version.trim().is_empty()
        || profile.runtime != binding.runtime
    {
        return Err(CommandFailure::invalid_input(format!(
            "{:?} live runtime binding drifted",
            profile.variant
        )));
    }
    profile.runtime_digest = binding.runtime_digest.clone();
    profile.model_identifier = model_identifier.to_string();
    profile.model_digest = model_digest.clone();
    profile.adapter_version.clone_from(&binding.adapter_version);
    profile.adapter_digest = binding.adapter_digest.clone();
    Ok(())
}
