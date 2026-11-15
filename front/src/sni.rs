//! Reads the server name from a TLS ClientHello without ending TLS.
//!
//! The front sees only the first TLS record. It needs the hostname and
//! nothing else, so this parser walks just far enough to find the
//! `server_name` extension (RFC 6066 section 3) and never looks at the rest.

/// Largest first record the front will buffer: a TLS record is at most
/// 2^14 bytes of payload plus its 5-byte header.
pub const MAX_RECORD: usize = 5 + (1 << 14);

#[derive(Debug, PartialEq, Eq)]
pub enum Parse {
    /// More bytes are needed; at least this many in total.
    Incomplete(usize),
    /// The hostname, lowercased.
    Name(String),
    /// Not a ClientHello with a usable server name.
    Invalid(&'static str),
}

struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl<'a> Reader<'a> {
    fn new(buf: &'a [u8]) -> Self {
        Reader { buf, pos: 0 }
    }
    fn take(&mut self, n: usize) -> Option<&'a [u8]> {
        let end = self.pos.checked_add(n)?;
        let out = self.buf.get(self.pos..end)?;
        self.pos = end;
        Some(out)
    }
    fn u8(&mut self) -> Option<u8> {
        self.take(1).map(|b| b[0])
    }
    fn u16(&mut self) -> Option<usize> {
        self.take(2)
            .map(|b| u16::from_be_bytes([b[0], b[1]]) as usize)
    }
    fn u24(&mut self) -> Option<usize> {
        self.take(3)
            .map(|b| ((b[0] as usize) << 16) | ((b[1] as usize) << 8) | b[2] as usize)
    }
    fn vec8(&mut self) -> Option<&'a [u8]> {
        let n = self.u8()? as usize;
        self.take(n)
    }
    fn vec16(&mut self) -> Option<&'a [u8]> {
        let n = self.u16()?;
        self.take(n)
    }
}

/// Parses the first TLS record in `buf`.
pub fn server_name(buf: &[u8]) -> Parse {
    if buf.len() < 5 {
        return Parse::Incomplete(5);
    }
    // Record header: handshake (22), legacy version 3.x, length.
    if buf[0] != 22 {
        return Parse::Invalid("not a TLS handshake");
    }
    if buf[1] != 3 {
        return Parse::Invalid("not TLS");
    }
    let len = u16::from_be_bytes([buf[3], buf[4]]) as usize;
    if len == 0 || 5 + len > MAX_RECORD {
        return Parse::Invalid("bad record length");
    }
    if buf.len() < 5 + len {
        return Parse::Incomplete(5 + len);
    }
    let mut r = Reader::new(&buf[5..5 + len]);
    match client_hello(&mut r) {
        Some(Ok(name)) => Parse::Name(name),
        Some(Err(why)) => Parse::Invalid(why),
        // A ClientHello split across records is legal but no client the
        // front serves sends one; refuse rather than buffer more.
        None => Parse::Invalid("truncated ClientHello"),
    }
}

fn client_hello(r: &mut Reader) -> Option<Result<String, &'static str>> {
    if r.u8()? != 1 {
        return Some(Err("not a ClientHello"));
    }
    let body_len = r.u24()?;
    let mut body = Reader::new(r.take(body_len)?);
    body.take(2)?; // legacy_version
    body.take(32)?; // random
    body.vec8()?; // legacy_session_id
    body.vec16()?; // cipher_suites
    body.vec8()?; // legacy_compression_methods
    if body.pos == body.buf.len() {
        return Some(Err("no extensions"));
    }
    let mut exts = Reader::new(body.vec16()?);
    let mut found: Option<String> = None;
    while exts.pos < exts.buf.len() {
        let kind = exts.u16()?;
        let data = exts.vec16()?;
        if kind != 0 {
            continue;
        }
        if found.is_some() {
            return Some(Err("two server_name extensions"));
        }
        let mut list = Reader::new(data);
        let mut names = Reader::new(list.vec16()?);
        while names.pos < names.buf.len() {
            let name_type = names.u8()?;
            let name = names.vec16()?;
            if name_type != 0 {
                continue;
            }
            if found.is_some() {
                return Some(Err("two host names"));
            }
            match host_name(name) {
                Ok(n) => found = Some(n),
                Err(why) => return Some(Err(why)),
            }
        }
    }
    Some(found.ok_or("no server name"))
}

fn host_name(raw: &[u8]) -> Result<String, &'static str> {
    if raw.is_empty() || raw.len() > 253 {
        return Err("bad host name length");
    }
    let ok = raw
        .iter()
        .all(|&b| b.is_ascii_alphanumeric() || b == b'-' || b == b'.');
    if !ok {
        return Err("bad host name");
    }
    Ok(String::from_utf8_lossy(raw).to_ascii_lowercase())
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// Builds a minimal ClientHello record, optionally with a server name
    /// and extra extensions.
    pub fn client_hello_record(name: Option<&str>, extra_ext: &[(u16, Vec<u8>)]) -> Vec<u8> {
        let mut exts = Vec::new();
        if let Some(name) = name {
            let mut entry = vec![0u8];
            entry.extend_from_slice(&(name.len() as u16).to_be_bytes());
            entry.extend_from_slice(name.as_bytes());
            let mut list = (entry.len() as u16).to_be_bytes().to_vec();
            list.extend(entry);
            exts.extend_from_slice(&0u16.to_be_bytes());
            exts.extend_from_slice(&(list.len() as u16).to_be_bytes());
            exts.extend(list);
        }
        for (kind, data) in extra_ext {
            exts.extend_from_slice(&kind.to_be_bytes());
            exts.extend_from_slice(&(data.len() as u16).to_be_bytes());
            exts.extend_from_slice(data);
        }
        let mut body = vec![3, 3];
        body.extend_from_slice(&[7u8; 32]);
        body.push(0); // session id
        body.extend_from_slice(&[0, 2, 0x13, 0x01]); // one cipher suite
        body.extend_from_slice(&[1, 0]); // null compression
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

    #[test]
    fn reads_the_server_name() {
        let rec = client_hello_record(
            Some("Abc.Agents.Isoform.AI"),
            &[(16, vec![0, 3, 2, b'h', b'2'])],
        );
        assert_eq!(
            server_name(&rec),
            Parse::Name("abc.agents.isoform.ai".into())
        );
    }

    #[test]
    fn asks_for_more_until_the_record_is_complete() {
        let rec = client_hello_record(Some("a.example"), &[]);
        assert_eq!(server_name(&rec[..3]), Parse::Incomplete(5));
        assert_eq!(server_name(&rec[..10]), Parse::Incomplete(rec.len()));
        assert_eq!(server_name(&rec), Parse::Name("a.example".into()));
    }

    #[test]
    fn refuses_what_is_not_a_client_hello_with_a_name() {
        assert_eq!(
            server_name(b"GET / HTTP/1.1\r\n"),
            Parse::Invalid("not a TLS handshake")
        );
        assert_eq!(
            server_name(&client_hello_record(None, &[])),
            Parse::Invalid("no server name")
        );
        assert_eq!(
            server_name(&client_hello_record(Some("bad_name.example"), &[])),
            Parse::Invalid("bad host name")
        );
        // A record missing its last byte waits for it.
        let rec = client_hello_record(Some("a.example"), &[]);
        assert_eq!(
            server_name(&rec[..rec.len() - 1]),
            Parse::Incomplete(rec.len())
        );
    }

    #[test]
    fn never_reads_past_a_lying_length() {
        let mut rec = client_hello_record(Some("a.example"), &[]);
        // Claim a longer handshake body than the record holds.
        rec[6] = 0xff;
        assert_eq!(server_name(&rec), Parse::Invalid("truncated ClientHello"));
        // Random bytes after a valid header never panic.
        for seed in 0..500u32 {
            let mut junk = vec![22, 3, 1, 0, 64];
            junk.extend(
                (0..64).map(|i| (seed.wrapping_mul(2654435761).wrapping_add(i * 97) >> 7) as u8),
            );
            let _ = server_name(&junk);
        }
    }
}
