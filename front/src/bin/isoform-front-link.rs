//! `isoform-front-link`: the computer's side of the front.
//!
//!   isoform-front-link --key-file <path> --allow <front id>[,<front id>...]
//!       --forward 127.0.0.1:<tls port> [--suffix agents.isoform.ai]
//!
//! Prints one JSON line when ready: {"endpointId", "label", "hostname"}.
//! The app starts it, reads that line, and serves TLS for the hostname on
//! the forward port.

use std::collections::HashSet;
use std::net::SocketAddr;
use std::path::PathBuf;

use anyhow::{bail, Context};
use iroh::{endpoint::presets, Endpoint, EndpointId};
use isoform_front::{keyfile::load_or_make_key, label, link::Link, ALPN};

struct Args {
    key_file: PathBuf,
    allow: HashSet<EndpointId>,
    forward: SocketAddr,
    suffix: String,
}

fn parse_args() -> anyhow::Result<Args> {
    let mut key_file = None;
    let mut allow = HashSet::new();
    let mut forward = None;
    let mut suffix = "agents.isoform.ai".to_string();
    let mut it = std::env::args().skip(1);
    while let Some(flag) = it.next() {
        let value = it.next().with_context(|| format!("{flag} needs a value"))?;
        match flag.as_str() {
            "--key-file" => key_file = Some(PathBuf::from(value)),
            "--allow" => {
                for id in value.split(',').filter(|s| !s.trim().is_empty()) {
                    allow.insert(
                        id.trim()
                            .parse::<EndpointId>()
                            .with_context(|| format!("bad front id {id}"))?,
                    );
                }
            }
            "--forward" => forward = Some(value.parse::<SocketAddr>().context("bad --forward")?),
            "--suffix" => suffix = value.trim().trim_matches('.').to_ascii_lowercase(),
            other => bail!("unknown flag {other}"),
        }
    }
    Ok(Args {
        key_file: key_file.context("--key-file is required")?,
        allow,
        forward: forward.context("--forward is required")?,
        suffix,
    })
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = parse_args()?;
    let link = Link::new(args.allow, args.forward)?;
    let key = load_or_make_key(&args.key_file)?;
    let endpoint = Endpoint::builder(presets::N0)
        .secret_key(key)
        .alpns(vec![ALPN.to_vec()])
        .bind()
        .await
        .context("binding the iroh endpoint")?;
    let id = endpoint.id();
    let lbl = label::encode(&id);
    println!(
        "{{\"endpointId\":\"{id}\",\"label\":\"{lbl}\",\"hostname\":\"{lbl}.{}\"}}",
        args.suffix
    );
    let serving = tokio::spawn(link.serve(endpoint.clone()));
    tokio::select! {
        _ = serving => {}
        _ = tokio::signal::ctrl_c() => {}
    }
    endpoint.close().await;
    Ok(())
}
