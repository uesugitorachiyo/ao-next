use std::fmt;

use serde::de::{DeserializeOwned, Error as _, MapAccess, SeqAccess, Visitor};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};
use sha2::{Digest as _, Sha256};
use thiserror::Error;

use crate::contracts::Digest;

#[derive(Debug, Error, PartialEq, Eq)]
pub enum StrictJsonError {
    #[error("input is oversized: {actual} bytes exceeds {limit}")]
    Oversized { actual: usize, limit: usize },
    #[error("duplicate JSON key: {0}")]
    DuplicateKey(String),
    #[error("malformed JSON: {0}")]
    Malformed(String),
    #[error("contract deserialization failed: {0}")]
    Deserialize(String),
    #[error("canonical serialization failed: {0}")]
    Serialize(String),
}

#[derive(Debug)]
struct UniqueValue(Value);

impl<'de> Deserialize<'de> for UniqueValue {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(UniqueValueVisitor)
    }
}

struct UniqueValueVisitor;

impl<'de> Visitor<'de> for UniqueValueVisitor {
    type Value = UniqueValue;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("a JSON value with unique object keys")
    }

    fn visit_bool<E>(self, value: bool) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::Bool(value)))
    }

    fn visit_i64<E>(self, value: i64) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::Number(value.into())))
    }

    fn visit_u64<E>(self, value: u64) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::Number(value.into())))
    }

    fn visit_f64<E>(self, value: f64) -> Result<Self::Value, E>
    where
        E: serde::de::Error,
    {
        serde_json::Number::from_f64(value)
            .map(Value::Number)
            .map(UniqueValue)
            .ok_or_else(|| E::custom("non-finite JSON number"))
    }

    fn visit_str<E>(self, value: &str) -> Result<Self::Value, E>
    where
        E: serde::de::Error,
    {
        Ok(UniqueValue(Value::String(value.to_owned())))
    }

    fn visit_string<E>(self, value: String) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::String(value)))
    }

    fn visit_none<E>(self) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::Null))
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E> {
        Ok(UniqueValue(Value::Null))
    }

    fn visit_some<D>(self, deserializer: D) -> Result<Self::Value, D::Error>
    where
        D: Deserializer<'de>,
    {
        UniqueValue::deserialize(deserializer)
    }

    fn visit_seq<A>(self, mut sequence: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        let mut values = Vec::new();
        while let Some(value) = sequence.next_element::<UniqueValue>()? {
            values.push(value.0);
        }
        Ok(UniqueValue(Value::Array(values)))
    }

    fn visit_map<A>(self, mut object: A) -> Result<Self::Value, A::Error>
    where
        A: MapAccess<'de>,
    {
        let mut values = Map::new();
        while let Some(key) = object.next_key::<String>()? {
            if values.contains_key(&key) {
                return Err(A::Error::custom(format!("duplicate key `{key}`")));
            }
            let value = object.next_value::<UniqueValue>()?;
            values.insert(key, value.0);
        }
        Ok(UniqueValue(Value::Object(values)))
    }
}

/// Decodes bounded JSON while rejecting duplicate keys before typed deserialization.
///
/// # Errors
///
/// Returns [`StrictJsonError`] for oversized or malformed JSON, duplicate keys,
/// trailing data, unknown typed fields, and other contract deserialization failures.
pub fn decode_strict_json<T>(bytes: &[u8], max_bytes: usize) -> Result<T, StrictJsonError>
where
    T: DeserializeOwned,
{
    if bytes.len() > max_bytes {
        return Err(StrictJsonError::Oversized {
            actual: bytes.len(),
            limit: max_bytes,
        });
    }

    let mut deserializer = serde_json::Deserializer::from_slice(bytes);
    let value = UniqueValue::deserialize(&mut deserializer)
        .map_err(|error| classify_parse_error(&error))?;
    deserializer
        .end()
        .map_err(|error| StrictJsonError::Malformed(error.to_string()))?;
    serde_json::from_value(value.0).map_err(|error| StrictJsonError::Deserialize(error.to_string()))
}

fn classify_parse_error(error: &serde_json::Error) -> StrictJsonError {
    let message = error.to_string();
    if let Some(start) = message.find("duplicate key `") {
        let remainder = &message[start + "duplicate key `".len()..];
        if let Some(end) = remainder.find('`') {
            return StrictJsonError::DuplicateKey(remainder[..end].to_owned());
        }
    }
    StrictJsonError::Malformed(message)
}

/// Serializes a value as compact JSON with lexicographically ordered object keys.
///
/// # Errors
///
/// Returns [`StrictJsonError::Serialize`] when the value cannot be represented
/// as JSON.
pub fn canonical_json_bytes<T>(value: &T) -> Result<Vec<u8>, StrictJsonError>
where
    T: Serialize,
{
    let value = serde_json::to_value(value)
        .map_err(|error| StrictJsonError::Serialize(error.to_string()))?;
    serde_json::to_vec(&value).map_err(|error| StrictJsonError::Serialize(error.to_string()))
}

/// Calculates the lowercase `sha256:` digest of canonical JSON bytes.
///
/// # Errors
///
/// Returns [`StrictJsonError::Serialize`] when canonical serialization fails.
pub fn canonical_digest<T>(value: &T) -> Result<Digest, StrictJsonError>
where
    T: Serialize,
{
    let bytes = canonical_json_bytes(value)?;
    let hex = format!("{:x}", Sha256::digest(bytes));
    Digest::new(format!("sha256:{hex}"))
        .map_err(|error| StrictJsonError::Serialize(error.to_string()))
}
