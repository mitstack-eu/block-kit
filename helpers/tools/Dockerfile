# The kit's tools image holds each tool a block reads while it runs whose publisher ships no image of its own,
# fetched here once and checked against tools.sha256, copy-tool.sh, and the sign-in companion a Docker host runs from
# it. Each tool sits in a layer of its own, so a reader over the registry's HTTP API reads only the layer it needs.
# Build it from the repository's root, which holds the companion's source:
# docker build --platform linux/amd64 -f helpers/tools/Dockerfile -t block-kit-tools .
FROM docker.io/library/alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f AS fetch
RUN apk add --no-cache curl
COPY helpers/tools/tools.sha256 /tools.sha256
WORKDIR /out

FROM fetch AS age
RUN curl -fsSL --proto '=https' https://github.com/FiloSottile/age/releases/download/v1.3.2/age-v1.3.2-linux-amd64.tar.gz \
    | tar -xz age/age age/age-keygen \
 && grep '  age/' /tools.sha256 | sha256sum -c -

FROM fetch AS kubescape
RUN for file in nsa default_config_inputs; do \
      curl -fsSL --proto '=https' --create-dirs -o "kubescape/$file" "https://github.com/kubescape/regolibrary/releases/download/v2.0.38/$file" || exit 1; \
    done \
 && grep '  kubescape/' /tools.sha256 | sha256sum -c -

FROM fetch AS k3s
RUN curl -fsSL --proto '=https' --create-dirs -o k3s/install.sh \
      https://raw.githubusercontent.com/k3s-io/k3s/2d0f82fa2f933cd227fe38e1482558ce4769f464/install.sh \
 && grep '  k3s/' /tools.sha256 | sha256sum -c -

# kube-bench's releases stop at k3s-cis-1.9, so its config comes from the commit that added k3s-cis-1.11, and
# tools.sha256 lists each file it fetches.
FROM fetch AS kube-bench
RUN grep '  kube-bench/' /tools.sha256 | while read -r _ file; do \
      curl -fsSL --proto '=https' --create-dirs -o "$file" \
        "https://raw.githubusercontent.com/aquasecurity/kube-bench/e278a5915a982f2756026de6e2f9ca77077ef9f9/${file#kube-bench/}" || exit 1; \
    done \
 && grep '  kube-bench/' /tools.sha256 | sha256sum -c -

# The companion verifies mitstack's certificate against the CA bundle of the pinned alpine image, which no package
# install has touched, and is built with no build id or local path in it.
FROM docker.io/library/alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f AS ca
COPY helpers/tools/tools.sha256 /tools.sha256
RUN mkdir -p /out/mitstack-signin \
 && cp /etc/ssl/certs/ca-certificates.crt /out/mitstack-signin/ \
 && cd /out && grep '  mitstack-signin/' /tools.sha256 | sha256sum -c -

FROM docker.io/library/golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS signin
WORKDIR /src
COPY go.mod ./
COPY helpers/signin helpers/signin
COPY cmd/mitstack-signin cmd/mitstack-signin
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags='-s -w -buildid=' \
      -o /out/mitstack-signin/mitstack-signin ./cmd/mitstack-signin
COPY --from=ca /out/mitstack-signin/ca-certificates.crt /out/mitstack-signin/

FROM scratch
COPY --from=age /out/age /age
COPY --from=kubescape /out/kubescape /kubescape
COPY --from=k3s /out/k3s /k3s
COPY --from=kube-bench /out/kube-bench /kube-bench
COPY helpers/tools/scripts/copy-tool.sh /copy-tool/copy-tool.sh
COPY --from=signin /out/mitstack-signin /mitstack-signin
