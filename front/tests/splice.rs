//! End to end on loopback: a TLS-looking client connects to the front with
//! a label's hostname; the front dials that endpoint over iroh with the
//! front ALPN and the bytes come back unchanged. Nothing here uses a relay
//! or the internet.

use std::sync::atomic::Ordering;
use std::time::Duration;

use iroh::address_lookup::memory::MemoryLookup;
use iroh::endpoint::presets;
use iroh::{Endpoint, RelayMode, SecretKey};
use isoform_front::{label, Config, Front, ALPN};
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
async fn splices_a_label_to_its_endpoint_and_refuses_others() {
    // The person's computer: accepts the front ALPN and echoes, the way TLS
    // would answer, after checking who dialed.
    let computer = local_endpoint(vec![ALPN.to_vec()], None).await;
    let lookup = MemoryLookup::new();
    lookup.add_endpoint_info(computer.addr());
    let front_ep = local_endpoint(vec![], Some(lookup)).await;
    let front_id = front_ep.id();
    let accept = computer.clone();
    tokio::spawn(async move {
        while let Some(incoming) = accept.accept().await {
            let conn = incoming.await.unwrap();
            assert_eq!(conn.remote_id(), front_id, "only the front dials");
            tokio::spawn(async move {
                let (mut send, mut recv) = conn.accept_bi().await.unwrap();
                let mut buf = [0u8; 4096];
                loop {
                    let n = match recv.read(&mut buf).await {
                        Ok(Some(n)) if n > 0 => n,
                        _ => break,
                    };
                    send.write_all(&buf[..n]).await.unwrap();
                }
                let _ = send.finish();
                conn.closed().await;
            });
        }
    });

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

    // A label that names the computer: everything round-trips.
    let host = format!("{}.agents.example.test", label::encode(&computer.id()));
    let hello = client_hello(&host);
    let mut tcp = TcpStream::connect(addr).await.unwrap();
    tcp.write_all(&hello).await.unwrap();
    tcp.write_all(b"after the hello").await.unwrap();
    let mut got = vec![0u8; hello.len() + 15];
    tokio::time::timeout(Duration::from_secs(20), tcp.read_exact(&mut got))
        .await
        .expect("echo in time")
        .unwrap();
    assert_eq!(&got[..hello.len()], &hello[..]);
    assert_eq!(&got[hello.len()..], b"after the hello");
    drop(tcp);

    // Wrong domain and plain HTTP: closed without a dial.
    for first in [
        client_hello(&format!("{}.evil.example", label::encode(&computer.id()))),
        b"GET / HTTP/1.1\r\nHost: x\r\n\r\n".to_vec(),
    ] {
        let mut tcp = TcpStream::connect(addr).await.unwrap();
        tcp.write_all(&first).await.unwrap();
        let mut buf = [0u8; 8];
        let n = tokio::time::timeout(Duration::from_secs(5), tcp.read(&mut buf))
            .await
            .unwrap()
            .unwrap_or(0);
        assert_eq!(n, 0, "refused connections get no bytes");
    }
    assert_eq!(front.metrics.spliced.load(Ordering::Relaxed), 1);
    assert_eq!(front.metrics.bad_label.load(Ordering::Relaxed), 1);
    assert_eq!(front.metrics.bad_hello.load(Ordering::Relaxed), 1);
}

fn proxy_v2(client: [u8; 4]) -> Vec<u8> {
    let mut h = isoform_front::proxyproto::SIGNATURE.to_vec();
    h.extend_from_slice(&[0x21, 0x11, 0, 12]);
    h.extend_from_slice(&client);
    h.extend_from_slice(&[127, 0, 0, 1, 0x30, 0x39, 0x01, 0xbb]);
    h
}

#[tokio::test]
async fn behind_a_proxy_reads_the_proxy_header_then_splices() {
    let computer = local_endpoint(vec![ALPN.to_vec()], None).await;
    let lookup = MemoryLookup::new();
    lookup.add_endpoint_info(computer.addr());
    let front_ep = local_endpoint(vec![], Some(lookup)).await;
    let accept = computer.clone();
    tokio::spawn(async move {
        while let Some(incoming) = accept.accept().await {
            let conn = incoming.await.unwrap();
            tokio::spawn(async move {
                let (mut send, mut recv) = conn.accept_bi().await.unwrap();
                let mut buf = [0u8; 4096];
                while let Ok(Some(n)) = recv.read(&mut buf).await {
                    if n == 0 {
                        break;
                    }
                    send.write_all(&buf[..n]).await.unwrap();
                }
                let _ = send.finish();
                conn.closed().await;
            });
        }
    });
    let front = Front::new(
        Config {
            suffix: "agents.example.test".into(),
            proxy_protocol: true,
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

    // The proxy's header, then the client's hello: the hello reaches the
    // computer and the header does not.
    let hello = client_hello(&format!(
        "{}.agents.example.test",
        label::encode(&computer.id())
    ));
    let mut tcp = TcpStream::connect(addr).await.unwrap();
    tcp.write_all(&proxy_v2([203, 0, 113, 9])).await.unwrap();
    tcp.write_all(&hello).await.unwrap();
    let mut got = vec![0u8; hello.len()];
    tokio::time::timeout(Duration::from_secs(20), tcp.read_exact(&mut got))
        .await
        .expect("echo in time")
        .unwrap();
    assert_eq!(got, hello);
    drop(tcp);

    // No header where one is required: closed without a dial.
    let mut tcp = TcpStream::connect(addr).await.unwrap();
    tcp.write_all(&hello).await.unwrap();
    let mut buf = [0u8; 8];
    let n = tokio::time::timeout(Duration::from_secs(5), tcp.read(&mut buf))
        .await
        .unwrap()
        .unwrap_or(0);
    assert_eq!(n, 0);
    assert_eq!(front.metrics.spliced.load(Ordering::Relaxed), 1);
}
