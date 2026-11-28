FROM ghcr.io/logto-io/logto:1.46.0@sha256:d5e208ce705cbcf7c79ddba421216eb65413d0b0531aa658fb22a4ba659be983
WORKDIR /etc/logto
RUN rm -rf packages/core/build packages/console/dist
ADD logto-1.46.0-admin-auth-overlay.tar.gz /etc/logto/
