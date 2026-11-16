# The Isoform front on Fly (docs/front-deploy.md). Rust is pinned by
# rust-toolchain.toml; the image matches it.
FROM rust:1.93.1-bookworm AS build
WORKDIR /src
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY src ./src
RUN cargo build --release --locked --bin isoform-front

# glibc and CA roots (iroh's relays use TLS). It runs as root so it can make
# its key file on the Fly volume at /data; the machine is its own VM.
FROM gcr.io/distroless/cc-debian12
COPY --from=build /src/target/release/isoform-front /isoform-front
EXPOSE 8443
ENTRYPOINT ["/isoform-front"]
