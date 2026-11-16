//! `isoform-front`: listens on TCP (443 in production) and passes each TLS
//! connection, still encrypted, to the person's computer over iroh.
//!
//! Settings come from the environment:
//!   ISOFORM_FRONT_LISTEN    address to listen on (default 0.0.0.0:443)
//!   ISOFORM_FRONT_SUFFIX    hostname suffix (default agents.isoform.ai)
//!   ISOFORM_FRONT_KEY_FILE  the front's iroh secret key; made (0600) when
//!                           missing. Computers accept only this key's id.
//!   ISOFORM_FRONT_PROXY_PROTOCOL  "v2" when a proxy (Fly) puts a PROXY v2
//!                           header before each connection.

use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Context;
use iroh::{endpoint::presets, Endpoint};
use isoform_front::{keyfile::load_or_make_key, Config, Front};
use tokio::net::TcpListener;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let listen = std::env::var("ISOFORM_FRONT_LISTEN").unwrap_or_else(|_| "0.0.0.0:443".into());
    let key_file = std::env::var("ISOFORM_FRONT_KEY_FILE").unwrap_or_else(|_| "front.key".into());
    let mut cfg = Config::default();
    if let Ok(suffix) = std::env::var("ISOFORM_FRONT_SUFFIX") {
        cfg.suffix = suffix.trim().trim_matches('.').to_ascii_lowercase();
    }
    cfg.proxy_protocol =
        std::env::var("ISOFORM_FRONT_PROXY_PROTOCOL").is_ok_and(|v| v.trim() == "v2");
    let key = load_or_make_key(Path::new(&key_file))?;
    let endpoint = Endpoint::builder(presets::N0)
        .secret_key(key)
        .bind()
        .await
        .context("binding the iroh endpoint")?;
    // Computers add this id to the front ids they accept.
    println!("front endpoint id: {}", endpoint.id());
    let listener = TcpListener::bind(&listen)
        .await
        .with_context(|| format!("listening on {listen}"))?;
    println!("listening on {listen} for *.{}", cfg.suffix);
    let front = Front::new(cfg, endpoint);

    let reporter = Arc::clone(&front);
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(60));
        loop {
            tick.tick().await;
            reporter.sweep();
            println!("{}", reporter.metrics.line());
        }
    });

    loop {
        tokio::select! {
            accepted = listener.accept() => {
                match accepted {
                    Ok((tcp, peer)) => {
                        tokio::spawn(Arc::clone(&front).handle(tcp, peer));
                    }
                    Err(err) => {
                        eprintln!("accept: {err}");
                        tokio::time::sleep(Duration::from_millis(50)).await;
                    }
                }
            }
            _ = tokio::signal::ctrl_c() => {
                println!("{}", front.metrics.line());
                return Ok(());
            }
        }
    }
}
