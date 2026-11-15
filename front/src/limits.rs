//! Rate limits kept in memory only: token buckets per key and a count of
//! open connections per label. Nothing is written down.

use std::collections::HashMap;
use std::hash::Hash;
use std::time::{Duration, Instant};

#[derive(Clone, Copy, Debug)]
pub struct Rate {
    /// Tokens added per second.
    pub per_sec: f64,
    /// Bucket size.
    pub burst: f64,
}

struct Bucket {
    tokens: f64,
    at: Instant,
}

/// Token buckets per key. Idle buckets are swept so a flood of random keys
/// can't grow the map without bound.
pub struct Buckets<K> {
    rate: Rate,
    max_keys: usize,
    map: HashMap<K, Bucket>,
}

impl<K: Hash + Eq + Clone> Buckets<K> {
    pub fn new(rate: Rate, max_keys: usize) -> Self {
        Buckets {
            rate,
            max_keys,
            map: HashMap::new(),
        }
    }

    /// Takes `cost` tokens for `key` at `now`; false when over the limit.
    pub fn take(&mut self, key: &K, cost: f64, now: Instant) -> bool {
        if !self.map.contains_key(key) && self.map.len() >= self.max_keys {
            self.sweep(now);
            if self.map.len() >= self.max_keys {
                return false;
            }
        }
        let rate = self.rate;
        let b = self.map.entry(key.clone()).or_insert(Bucket {
            tokens: rate.burst,
            at: now,
        });
        let elapsed = now.saturating_duration_since(b.at).as_secs_f64();
        b.tokens = (b.tokens + elapsed * rate.per_sec).min(rate.burst);
        b.at = now;
        if b.tokens >= cost {
            b.tokens -= cost;
            true
        } else {
            false
        }
    }

    /// Drops buckets that have refilled completely.
    pub fn sweep(&mut self, now: Instant) {
        let rate = self.rate;
        let full = Duration::from_secs_f64(rate.burst / rate.per_sec.max(f64::MIN_POSITIVE));
        self.map
            .retain(|_, b| now.saturating_duration_since(b.at) < full);
    }

    pub fn len(&self) -> usize {
        self.map.len()
    }

    pub fn is_empty(&self) -> bool {
        self.map.is_empty()
    }
}

/// Open connections per key, capped.
pub struct Concurrent<K> {
    max: usize,
    map: HashMap<K, usize>,
}

impl<K: Hash + Eq + Clone> Concurrent<K> {
    pub fn new(max: usize) -> Self {
        Concurrent {
            max,
            map: HashMap::new(),
        }
    }
    pub fn acquire(&mut self, key: &K) -> bool {
        let n = self.map.entry(key.clone()).or_insert(0);
        if *n >= self.max {
            return false;
        }
        *n += 1;
        true
    }
    pub fn release(&mut self, key: &K) {
        if let Some(n) = self.map.get_mut(key) {
            *n = n.saturating_sub(1);
            if *n == 0 {
                self.map.remove(key);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_bucket_refills_at_its_rate() {
        let mut b = Buckets::new(
            Rate {
                per_sec: 1.0,
                burst: 2.0,
            },
            10,
        );
        let t0 = Instant::now();
        assert!(b.take(&"a", 1.0, t0));
        assert!(b.take(&"a", 1.0, t0));
        assert!(!b.take(&"a", 1.0, t0));
        assert!(b.take(&"a", 1.0, t0 + Duration::from_millis(1100)));
        // Keys are independent.
        assert!(b.take(&"b", 2.0, t0));
    }

    #[test]
    fn a_flood_of_keys_stays_bounded() {
        let mut b = Buckets::new(
            Rate {
                per_sec: 1.0,
                burst: 1.0,
            },
            3,
        );
        let t0 = Instant::now();
        for k in 0..3 {
            assert!(b.take(&k, 1.0, t0));
        }
        assert!(!b.take(&99, 1.0, t0), "a new key past the cap is refused");
        // Once the old buckets refill, they are swept and room comes back.
        assert!(b.take(&99, 1.0, t0 + Duration::from_secs(2)));
        assert!(b.len() <= 3);
    }

    #[test]
    fn concurrent_connections_are_capped_per_key() {
        let mut c = Concurrent::new(2);
        assert!(c.acquire(&"a"));
        assert!(c.acquire(&"a"));
        assert!(!c.acquire(&"a"));
        assert!(c.acquire(&"b"));
        c.release(&"a");
        assert!(c.acquire(&"a"));
    }
}
