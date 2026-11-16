//! Isoform front (docs/design/mcp-public-front.md).
//!
//! A cloud agent opens TLS to `<label>.agents.isoform.ai`. The front reads
//! only the hostname from the ClientHello, decodes the label to the
//! person's iroh endpoint id, dials it with the `isoform/front/1` ALPN and
//! splices bytes both ways. TLS ends on the person's computer; the front
//! never holds a certificate for a label and can't read requests, tokens or
//! tasks. It keeps no table (the route is in the name) and no logs of
//! labels or addresses: only counts.

pub mod keyfile;
pub mod label;
pub mod limits;
pub mod link;
pub mod proxyproto;
pub mod sni;

use std::net::{IpAddr, SocketAddr};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use iroh::{Endpoint, EndpointId};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::Semaphore;

use limits::{Buckets, Concurrent, Rate};

/// The ALPN the front dials and the computer accepts.
pub const ALPN: &[u8] = b"isoform/front/1";

#[derive(Clone, Debug)]
pub struct Config {
    /// Hostnames are `<label>.<suffix>`.
    pub suffix: String,
    /// New connections per label.
    pub per_label_conn: Rate,
    /// TLS handshakes per source address (a /64 for IPv6).
    pub per_ip_handshake: Rate,
    /// Open connections per label.
    pub per_label_open: usize,
    /// Open connections in all.
    pub global_open: usize,
    /// Bytes per minute per label, both directions together.
    pub per_label_bytes_per_min: f64,
    pub handshake_timeout: Duration,
    pub dial_timeout: Duration,
    pub idle_timeout: Duration,
    /// Bound on tracked keys per limiter.
    pub max_tracked: usize,
    /// Each connection starts with a PROXY protocol v2 header naming the
    /// client (Fly's proxy); limits then count the client, not the proxy.
    pub proxy_protocol: bool,
}

impl Default for Config {
    fn default() -> Self {
        Config {
            suffix: "agents.isoform.ai".into(),
            per_label_conn: Rate {
                per_sec: 5.0,
                burst: 20.0,
            },
            per_ip_handshake: Rate {
                per_sec: 20.0,
                burst: 60.0,
            },
            per_label_open: 32,
            global_open: 4096,
            // MCP is small JSON; 32 MB a minute is generous.
            per_label_bytes_per_min: 32.0 * 1024.0 * 1024.0,
            handshake_timeout: Duration::from_secs(10),
            dial_timeout: Duration::from_secs(10),
            idle_timeout: Duration::from_secs(300),
            max_tracked: 100_000,
            proxy_protocol: false,
        }
    }
}

/// Counts only: never a label, an address or a byte of traffic.
#[derive(Default, Debug)]
pub struct Metrics {
    pub accepted: AtomicU64,
    pub over_global: AtomicU64,
    pub over_ip: AtomicU64,
    pub bad_hello: AtomicU64,
    pub bad_label: AtomicU64,
    pub over_label: AtomicU64,
    pub dial_failed: AtomicU64,
    pub spliced: AtomicU64,
    pub over_bytes: AtomicU64,
    pub bytes: AtomicU64,
}

impl Metrics {
    pub fn line(&self) -> String {
        let g = |c: &AtomicU64| c.load(Ordering::Relaxed);
        format!(
            "accepted={} over_global={} over_ip={} bad_hello={} bad_label={} over_label={} dial_failed={} spliced={} over_bytes={} bytes={}",
            g(&self.accepted), g(&self.over_global), g(&self.over_ip), g(&self.bad_hello), g(&self.bad_label),
            g(&self.over_label), g(&self.dial_failed), g(&self.spliced), g(&self.over_bytes), g(&self.bytes)
        )
    }
}

struct State {
    per_ip: Buckets<IpAddr>,
    per_label: Buckets<EndpointId>,
    open: Concurrent<EndpointId>,
    bytes: Buckets<EndpointId>,
}

pub struct Front {
    cfg: Config,
    endpoint: Endpoint,
    state: Mutex<State>,
    global: Semaphore,
    pub metrics: Metrics,
}

/// Source key for the handshake limit: the address, or its /64 for IPv6.
fn source_key(ip: IpAddr) -> IpAddr {
    match ip {
        IpAddr::V4(v4) => IpAddr::V4(v4),
        IpAddr::V6(v6) => {
            if let Some(v4) = v6.to_ipv4_mapped() {
                return IpAddr::V4(v4);
            }
            let mut seg = v6.segments();
            seg[4..].fill(0);
            IpAddr::V6(seg.into())
        }
    }
}

impl Front {
    pub fn new(cfg: Config, endpoint: Endpoint) -> Arc<Self> {
        let bytes_rate = Rate {
            per_sec: cfg.per_label_bytes_per_min / 60.0,
            burst: cfg.per_label_bytes_per_min,
        };
        Arc::new(Front {
            state: Mutex::new(State {
                per_ip: Buckets::new(cfg.per_ip_handshake, cfg.max_tracked),
                per_label: Buckets::new(cfg.per_label_conn, cfg.max_tracked),
                open: Concurrent::new(cfg.per_label_open),
                bytes: Buckets::new(bytes_rate, cfg.max_tracked),
            }),
            global: Semaphore::new(cfg.global_open),
            cfg,
            endpoint,
            metrics: Metrics::default(),
        })
    }

    /// Serves one TCP connection to its end. Errors close the connection
    /// without a reply: the front can't answer inside TLS it doesn't hold.
    pub async fn handle(self: Arc<Self>, mut tcp: TcpStream, peer: SocketAddr) {
        self.metrics.accepted.fetch_add(1, Ordering::Relaxed);
        let Ok(_permit) = self.global.try_acquire() else {
            self.metrics.over_global.fetch_add(1, Ordering::Relaxed);
            return;
        };
        let mut source = peer.ip();
        if self.cfg.proxy_protocol {
            match tokio::time::timeout(self.cfg.handshake_timeout, proxyproto::read_v2(&mut tcp))
                .await
            {
                Ok(Ok(Some(client))) => source = client,
                // A LOCAL header is the proxy's own check: nothing to pass on.
                Ok(Ok(None)) => return,
                _ => {
                    self.metrics.bad_hello.fetch_add(1, Ordering::Relaxed);
                    return;
                }
            }
        }
        if !self
            .state
            .lock()
            .unwrap()
            .per_ip
            .take(&source_key(source), 1.0, Instant::now())
        {
            self.metrics.over_ip.fetch_add(1, Ordering::Relaxed);
            return;
        }
        let _ = tcp.set_nodelay(true);
        let hello =
            match tokio::time::timeout(self.cfg.handshake_timeout, read_client_hello(&mut tcp))
                .await
            {
                Ok(Ok(found)) => found,
                _ => {
                    self.metrics.bad_hello.fetch_add(1, Ordering::Relaxed);
                    return;
                }
            };
        let (buffered, host) = hello;
        let id = match label::decode(&host, &self.cfg.suffix) {
            Ok(id) => id,
            Err(_) => {
                self.metrics.bad_label.fetch_add(1, Ordering::Relaxed);
                return;
            }
        };
        {
            let mut st = self.state.lock().unwrap();
            if !st.per_label.take(&id, 1.0, Instant::now()) || !st.open.acquire(&id) {
                self.metrics.over_label.fetch_add(1, Ordering::Relaxed);
                return;
            }
        }
        let _open = OpenGuard { front: &self, id };
        let conn = match tokio::time::timeout(
            self.cfg.dial_timeout,
            self.endpoint.connect(id, ALPN),
        )
        .await
        {
            Ok(Ok(conn)) => conn,
            _ => {
                self.metrics.dial_failed.fetch_add(1, Ordering::Relaxed);
                return;
            }
        };
        let (mut send, recv) = match conn.open_bi().await {
            Ok(pair) => pair,
            Err(_) => {
                self.metrics.dial_failed.fetch_add(1, Ordering::Relaxed);
                return;
            }
        };
        if send.write_all(&buffered).await.is_err() {
            self.metrics.dial_failed.fetch_add(1, Ordering::Relaxed);
            return;
        }
        self.metrics.spliced.fetch_add(1, Ordering::Relaxed);
        let (tcp_read, tcp_write) = tcp.into_split();
        let up = self.pipe(tcp_read, send, id);
        let down = self.pipe(recv, tcp_write, id);
        // Either side ending ends both.
        tokio::select! {
            _ = up => {}
            _ = down => {}
        }
        conn.close(0u32.into(), b"done");
    }

    /// Copies one direction, charging the label's byte budget and closing
    /// on idle.
    async fn pipe<R, W>(&self, mut from: R, mut to: W, id: EndpointId)
    where
        R: AsyncRead + Unpin,
        W: AsyncWrite + Unpin,
    {
        let mut buf = vec![0u8; 16 * 1024];
        loop {
            let n = match tokio::time::timeout(self.cfg.idle_timeout, from.read(&mut buf)).await {
                Ok(Ok(0)) | Ok(Err(_)) | Err(_) => break,
                Ok(Ok(n)) => n,
            };
            if !self
                .state
                .lock()
                .unwrap()
                .bytes
                .take(&id, n as f64, Instant::now())
            {
                self.metrics.over_bytes.fetch_add(1, Ordering::Relaxed);
                break;
            }
            self.metrics.bytes.fetch_add(n as u64, Ordering::Relaxed);
            if to.write_all(&buf[..n]).await.is_err() {
                break;
            }
        }
        let _ = to.shutdown().await;
    }

    /// Sweeps idle limiter entries; call now and then.
    pub fn sweep(&self) {
        let now = Instant::now();
        let mut st = self.state.lock().unwrap();
        st.per_ip.sweep(now);
        st.per_label.sweep(now);
        st.bytes.sweep(now);
    }
}

struct OpenGuard<'a> {
    front: &'a Front,
    id: EndpointId,
}

impl Drop for OpenGuard<'_> {
    fn drop(&mut self) {
        self.front.state.lock().unwrap().open.release(&self.id);
    }
}

/// Reads until the first TLS record is complete and returns the bytes read
/// (to replay to the computer) and the hostname.
async fn read_client_hello<R: AsyncRead + Unpin>(r: &mut R) -> anyhow::Result<(Vec<u8>, String)> {
    let mut buf = Vec::with_capacity(2048);
    let mut chunk = [0u8; 4096];
    loop {
        match sni::server_name(&buf) {
            sni::Parse::Name(name) => return Ok((buf, name)),
            sni::Parse::Invalid(why) => anyhow::bail!(why),
            sni::Parse::Incomplete(want) => {
                if want > sni::MAX_RECORD {
                    anyhow::bail!("record too large");
                }
                let n = r.read(&mut chunk).await?;
                if n == 0 {
                    anyhow::bail!("closed before the ClientHello");
                }
                buf.extend_from_slice(&chunk[..n]);
                if buf.len() > sni::MAX_RECORD {
                    anyhow::bail!("record too large");
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::Ipv6Addr;

    #[test]
    fn ipv6_sources_share_a_slash_64() {
        let a: IpAddr = "2001:db8:1:2:aaaa::1".parse().unwrap();
        let b: IpAddr = "2001:db8:1:2:bbbb::9".parse().unwrap();
        let c: IpAddr = "2001:db8:1:3::1".parse().unwrap();
        assert_eq!(source_key(a), source_key(b));
        assert_ne!(source_key(a), source_key(c));
        let mapped = IpAddr::V6(Ipv6Addr::from([0, 0, 0, 0, 0, 0xffff, 0x0a00, 0x0001]));
        assert_eq!(source_key(mapped), "10.0.0.1".parse::<IpAddr>().unwrap());
    }

    #[tokio::test]
    async fn reads_a_hello_split_across_reads() {
        let rec = sni::tests::client_hello_record(Some("x.example"), &[]);
        let (mut a, mut b) = tokio::io::duplex(64);
        let w = rec.clone();
        tokio::spawn(async move {
            for part in w.chunks(7) {
                a.write_all(part).await.unwrap();
            }
        });
        let (buf, name) = read_client_hello(&mut b).await.unwrap();
        assert_eq!(name, "x.example");
        assert_eq!(buf, rec);
    }
}
