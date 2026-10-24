# The official runtime, uid, entrypoint, web assets, healthcheck and configuration
# remain inherited. Deployment keeps its existing verified web/config mounts.
FROM casbin/casdoor:v4.23.0@sha256:e13c9aada231f7338a03ab0ae7054d0a9480e77283338ab9fc319d88fe2b3ce0
LABEL org.opencontainers.image.licenses="Apache-2.0" \
      io.unihub.identity.upstream-commit="1b91d9d319a127e9b1af88fa7a20bb35c052b9d8" \
      io.unihub.identity.patch="audit-redaction-v1"
COPY --chown=1000:1000 --chmod=0755 server /server
COPY --chown=1000:1000 LICENSE NOTICE source-manifest.json /usr/share/licenses/unihub-casdoor/
