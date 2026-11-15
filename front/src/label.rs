//! Hostname to iroh endpoint id. `<label>.<suffix>`, where the label is the
//! endpoint id's 32 bytes in lowercase RFC 4648 base32 without padding (52
//! characters, one DNS label). The route is in the name, so the front keeps
//! no table.

use data_encoding::BASE32_NOPAD;
use iroh::EndpointId;

pub const LABEL_LEN: usize = 52;

/// Encodes an endpoint id as its label.
pub fn encode(id: &EndpointId) -> String {
    BASE32_NOPAD.encode(id.as_bytes()).to_ascii_lowercase()
}

/// Decodes `host` under `suffix` (for example `agents.isoform.ai`) to an
/// endpoint id. Anything else is refused before any dial.
pub fn decode(host: &str, suffix: &str) -> Result<EndpointId, &'static str> {
    let host = host.trim_end_matches('.');
    let rest = host
        .strip_suffix(suffix)
        .and_then(|r| r.strip_suffix('.'))
        .ok_or("wrong domain")?;
    if rest.len() != LABEL_LEN || rest.contains('.') {
        return Err("not a label");
    }
    if !rest
        .bytes()
        .all(|b| b.is_ascii_lowercase() || (b'2'..=b'7').contains(&b))
    {
        return Err("not a label");
    }
    let bytes = BASE32_NOPAD
        .decode(rest.to_ascii_uppercase().as_bytes())
        .map_err(|_| "not a label")?;
    let bytes: [u8; 32] = bytes.try_into().map_err(|_| "not a label")?;
    // A label must be its canonical encoding: no second name for one id.
    let id = EndpointId::from_bytes(&bytes).map_err(|_| "not a key")?;
    if encode(&id) != rest {
        return Err("not canonical");
    }
    Ok(id)
}

#[cfg(test)]
mod tests {
    use super::*;
    use iroh::SecretKey;

    const SUFFIX: &str = "agents.isoform.ai";

    #[test]
    fn round_trips_an_endpoint_id() {
        let id = SecretKey::generate().public();
        let label = encode(&id);
        assert_eq!(label.len(), LABEL_LEN);
        assert_eq!(decode(&format!("{label}.{SUFFIX}"), SUFFIX), Ok(id));
        assert_eq!(decode(&format!("{label}.{SUFFIX}."), SUFFIX), Ok(id));
    }

    #[test]
    fn refuses_other_names() {
        let label = encode(&SecretKey::generate().public());
        for host in [
            format!("{label}.evil.example"),
            format!("{label}x{SUFFIX}"),
            format!("x.{label}.{SUFFIX}"),
            format!("{}.{SUFFIX}", &label[..51]),
            format!("{}.{SUFFIX}", label.to_ascii_uppercase()),
            format!("{}1.{SUFFIX}", &label[..51]),
            SUFFIX.to_string(),
        ] {
            assert!(decode(&host, SUFFIX).is_err(), "{host} was accepted");
        }
    }
}
