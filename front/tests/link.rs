//! The front and the computer's link together on loopback: an agent's TLS
//! bytes reach the computer's loopback server through the front, and an
//! endpoint that isn't the front is refused by the link.

use std::collections::HashSet;
use std::sync::atomic::Ordering;
use std::time::Duration;

use iroh::address_lookup::memory::MemoryLookup;
use iroh::endpoint::presets;
use iroh::{Endpoint, RelayMode, SecretKey};
use isoform_front::{label, link::Link, Config, Front, ALPN};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

fn client_hello(name: &str) -> Vec<u8> {
    let mut entry = vec![0u8];
    entry.extend_from_slice(&(name.len() as u16).to_be_bytes());
    entry.extend_from_slice(name.as_bytes());
    let mut list = (entry.len() as u16).to_be_bytes().to_vec();
    list.extend(entry);
    let mut exts = 0u16.to_be_bytes().to_vec();
    exts.extend_from_slice(&(list.len() as u16).to_be_bytes());
    exts.extend(list);
    let mut body = vec![3, 3];
    body.extend_from_slice(&[9u8; 32]);
    body.push(0);
    body.extend_from_slice(&[0, 2, 0x13, 0x01, 1, 0]);
    body.extend_from_slice(&(exts.len() as u16).to_be_bytes());
    body.extend(exts);
    let mut hs = vec![1];
    hs.extend_from_slice(&(body.len() as u32).to_be_bytes()[1..]);
    hs.extend(body);
    let mut rec = vec![22, 3, 1];
    rec.extend_from_slice(&(hs.len() as u16).to_be_bytes());
    rec.extend(hs);
    rec
}

async fn local_endpoint(alpns: Vec<Vec<u8>>, lookup: Option<MemoryLookup>) -> Endpoint {
    let mut b = Endpoint::builder(presets::Minimal)
        .secret_key(SecretKey::generate())
        .relay_mode(RelayMode::Disabled)
        .alpns(alpns)
        .bind_addr("127.0.0.1:0")
        .unwrap();
    if let Some(lookup) = lookup {
        b = b.address_lookup(lookup);
    }
    b.bind().await.unwrap()
}

#[tokio::test]
async fn the_link_accepts_only_the_front_and_forwards_to_loopback() {
    // The computer's TLS server stand-in: echoes with a prefix.
    let tls = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let tls_addr = tls.local_addr().unwrap();
    tokio::spawn(async move {
        loop {
            let (mut s, _) = tls.accept().await.unwrap();
            tokio::spawn(async move {
                let mut buf = [0u8; 4096];
                loop {
                    let n = match s.read(&mut buf).await {
                        Ok(0) | Err(_) => break,
                        Ok(n) => n,
                    };
                    if s.write_all(&buf[..n]).await.is_err() {
                        break;
                    }
                }
            });
        }
    });

    let lookup = MemoryLookup::new();
    let front_ep = local_endpoint(vec![], Some(lookup.clone())).await;
    let computer = local_endpoint(vec![ALPN.to_vec()], None).await;
    lookup.add_endpoint_info(computer.addr());
    let link = Link::new(HashSet::from([front_ep.id()]), tls_addr).unwrap();
    tokio::spawn(link.clone().serve(computer.clone()));

    let front = Front::new(
        Config {
            suffix: "agents.example.test".into(),
            ..Config::default()
        },
        front_ep,
    );
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let serving = front.clone();
    tokio::spawn(async move {
        loop {
            let (tcp, peer) = listener.accept().await.unwrap();
            tokio::spawn(serving.clone().handle(tcp, peer));
        }
    });

    let hello = client_hello(&format!(
        "{}.agents.example.test",
        label::encode(&computer.id())
    ));
    let mut tcp = TcpStream::connect(addr).await.unwrap();
    tcp.write_all(&hello).await.unwrap();
    tcp.write_all(b"GET /t/x").await.unwrap();
    let mut got = vec![0u8; hello.len() + 8];
    tokio::time::timeout(Duration::from_secs(20), tcp.read_exact(&mut got))
        .await
        .expect("in time")
        .unwrap();
    assert_eq!(&got[hello.len()..], b"GET /t/x");
    assert_eq!(link.metrics.streams.load(Ordering::Relaxed), 1);

    // Any other endpoint is refused before a byte is forwarded.
    let stranger_lookup = MemoryLookup::new();
    stranger_lookup.add_endpoint_info(computer.addr());
    let stranger = local_endpoint(vec![], Some(stranger_lookup)).await;
    let conn = stranger.connect(computer.id(), ALPN).await.unwrap();
    if let Ok((mut send, mut recv)) = conn.open_bi().await {
        let _ = send.write_all(b"hello").await;
        let mut buf = [0u8; 4];
        let read = tokio::time::timeout(Duration::from_secs(5), recv.read(&mut buf)).await;
        assert!(
            !matches!(read, Ok(Ok(Some(n))) if n > 0),
            "a stranger got bytes back"
        );
    }
    tokio::time::timeout(Duration::from_secs(5), conn.closed())
        .await
        .expect("the link closes a stranger");
    assert_eq!(link.metrics.refused.load(Ordering::Relaxed), 1);
    assert_eq!(link.metrics.streams.load(Ordering::Relaxed), 1);
}

#[test]
fn the_link_refuses_to_forward_off_the_computer() {
    let ids = HashSet::from([SecretKey::generate().public()]);
    assert!(Link::new(ids.clone(), "10.0.0.5:8443".parse().unwrap()).is_err());
    assert!(Link::new(HashSet::new(), "127.0.0.1:8443".parse().unwrap()).is_err());
    assert!(Link::new(ids, "127.0.0.1:8443".parse().unwrap()).is_ok());
}
