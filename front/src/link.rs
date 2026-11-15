//! The computer's side of the front: `isoform-front-link`.
//!
//! It runs on the person's computer with its own iroh key, kept only for
//! this (never the phone link's), so its id is the public hostname label.
//! It accepts connections on the front ALPN **only from the front ids it is
//! given**, and forwards each stream, still TLS, to the computer's loopback
//! TLS server, where TLS ends. It reads nothing it forwards.

use std::collections::HashSet;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use iroh::{Endpoint, EndpointId};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::TcpStream;

use crate::ALPN;

#[derive(Default, Debug)]
pub struct LinkMetrics {
    pub refused: AtomicU64,
    pub streams: AtomicU64,
    pub forward_failed: AtomicU64,
}

pub struct Link {
    allowed: HashSet<EndpointId>,
    forward: SocketAddr,
    idle: Duration,
    pub metrics: LinkMetrics,
}

impl Link {
    /// `forward` must be a loopback address: the TLS server on this computer.
    pub fn new(allowed: HashSet<EndpointId>, forward: SocketAddr) -> anyhow::Result<Arc<Self>> {
        if !forward.ip().is_loopback() {
            anyhow::bail!("the link forwards only to a loopback address");
        }
        if allowed.is_empty() {
            anyhow::bail!("give at least one front id to accept");
        }
        Ok(Arc::new(Link {
            allowed,
            forward,
            idle: Duration::from_secs(300),
            metrics: LinkMetrics::default(),
        }))
    }

    /// Accepts connections until the endpoint closes.
    pub async fn serve(self: Arc<Self>, endpoint: Endpoint) {
        while let Some(incoming) = endpoint.accept().await {
            let link = Arc::clone(&self);
            tokio::spawn(async move {
                let Ok(conn) = incoming.await else { return };
                if conn.alpn() != ALPN || !link.allowed.contains(&conn.remote_id()) {
                    link.metrics.refused.fetch_add(1, Ordering::Relaxed);
                    conn.close(1u32.into(), b"not allowed");
                    return;
                }
                // The front opens one stream per agent connection.
                while let Ok((send, recv)) = conn.accept_bi().await {
                    let link = Arc::clone(&link);
                    tokio::spawn(async move { link.forward(send, recv).await });
                }
            });
        }
    }

    async fn forward<W, R>(&self, send: W, recv: R)
    where
        W: AsyncWrite + Unpin,
        R: AsyncRead + Unpin,
    {
        let tcp = match TcpStream::connect(self.forward).await {
            Ok(tcp) => tcp,
            Err(_) => {
                self.metrics.forward_failed.fetch_add(1, Ordering::Relaxed);
                return;
            }
        };
        self.metrics.streams.fetch_add(1, Ordering::Relaxed);
        let _ = tcp.set_nodelay(true);
        let (tcp_read, tcp_write) = tcp.into_split();
        tokio::select! {
            _ = pipe(recv, tcp_write, self.idle) => {}
            _ = pipe(tcp_read, send, self.idle) => {}
        }
    }
}

async fn pipe<R, W>(mut from: R, mut to: W, idle: Duration)
where
    R: AsyncRead + Unpin,
    W: AsyncWrite + Unpin,
{
    let mut buf = vec![0u8; 16 * 1024];
    loop {
        let n = match tokio::time::timeout(idle, from.read(&mut buf)).await {
            Ok(Ok(0)) | Ok(Err(_)) | Err(_) => break,
            Ok(Ok(n)) => n,
        };
        if to.write_all(&buf[..n]).await.is_err() {
            break;
        }
    }
    let _ = to.shutdown().await;
}
