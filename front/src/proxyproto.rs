//! PROXY protocol v2, as Fly's proxy sends it (`proxy_proto_options =
//! { version = "v2" }`): a binary header before the client's first byte that
//! names the client's address. Behind Fly every connection otherwise comes
//! from the proxy, and the per-source handshake limit would count everyone
//! as one source. Only the source address is kept; TLVs are skipped.

use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use anyhow::{bail, Context};
use tokio::io::{AsyncRead, AsyncReadExt};

/// The v2 signature.
pub const SIGNATURE: [u8; 12] = [
    0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
];

/// Bound on the address block, TLVs included.
const MAX_LEN: usize = 512;

/// Reads one v2 header and returns the client's address: `None` for a LOCAL
/// header (the proxy's own health check) or an address family it doesn't
/// carry. A missing or malformed header is an error: the connection closes.
pub async fn read_v2<R: AsyncRead + Unpin>(r: &mut R) -> anyhow::Result<Option<IpAddr>> {
    let mut head = [0u8; 16];
    r.read_exact(&mut head)
        .await
        .context("reading the PROXY header")?;
    if head[..12] != SIGNATURE {
        bail!("no PROXY v2 signature");
    }
    let version = head[12] >> 4;
    let command = head[12] & 0x0F;
    if version != 2 {
        bail!("PROXY version {version}");
    }
    let family = head[13];
    let len = u16::from_be_bytes([head[14], head[15]]) as usize;
    if len > MAX_LEN {
        bail!("PROXY header too long");
    }
    let mut block = vec![0u8; len];
    r.read_exact(&mut block)
        .await
        .context("reading the PROXY addresses")?;
    match command {
        0x0 => return Ok(None), // LOCAL
        0x1 => {}               // PROXY
        _ => bail!("PROXY command {command}"),
    }
    match family {
        // TCP over IPv4: source, destination, source port, destination port.
        0x11 if len >= 12 => Ok(Some(IpAddr::V4(Ipv4Addr::new(
            block[0], block[1], block[2], block[3],
        )))),
        // TCP over IPv6.
        0x21 if len >= 36 => {
            let mut a = [0u8; 16];
            a.copy_from_slice(&block[..16]);
            Ok(Some(IpAddr::V6(Ipv6Addr::from(a))))
        }
        0x11 | 0x21 => bail!("PROXY address block too short"),
        _ => Ok(None),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn header(cmd: u8, fam: u8, block: &[u8]) -> Vec<u8> {
        let mut h = SIGNATURE.to_vec();
        h.push(0x20 | cmd);
        h.push(fam);
        h.extend_from_slice(&(block.len() as u16).to_be_bytes());
        h.extend_from_slice(block);
        h
    }

    #[tokio::test]
    async fn reads_ipv4_and_ipv6_sources_and_leaves_the_hello() {
        let mut v4 = header(
            1,
            0x11,
            &[203, 0, 113, 7, 10, 0, 0, 1, 0x1f, 0x90, 0x01, 0xbb],
        );
        v4.extend_from_slice(b"\x16rest");
        let mut r = &v4[..];
        assert_eq!(
            read_v2(&mut r).await.unwrap(),
            Some("203.0.113.7".parse().unwrap())
        );
        assert_eq!(r, b"\x16rest");

        let mut block = vec![0u8; 36];
        block[0] = 0x20;
        block[1] = 0x01;
        block[2] = 0x0d;
        block[3] = 0xb8;
        let v6 = header(1, 0x21, &block);
        let mut r = &v6[..];
        assert_eq!(
            read_v2(&mut r).await.unwrap(),
            Some("2001:db8::".parse().unwrap())
        );
    }

    #[tokio::test]
    async fn local_is_no_address_and_garbage_is_refused() {
        let local = header(0, 0x00, &[]);
        assert_eq!(read_v2(&mut &local[..]).await.unwrap(), None);
        assert!(read_v2(&mut &b"\x16\x03\x01 not a proxy header"[..])
            .await
            .is_err());
        assert!(read_v2(&mut &header(1, 0x11, &[1, 2, 3])[..])
            .await
            .is_err());
        let mut long = SIGNATURE.to_vec();
        long.extend_from_slice(&[0x21, 0x11, 0xff, 0xff]);
        assert!(read_v2(&mut &long[..]).await.is_err());
    }
}
