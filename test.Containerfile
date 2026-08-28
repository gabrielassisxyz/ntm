# Image for running this repository's test suite away from the machine it is
# developed on.
#
# The suite drives real tmux and has twice damaged its host: it deleted the
# real ~/.config in August, and it killed the operator's live, attached tmux
# server. Containment attempts that stayed on the host all failed for the same
# reason — they substituted the tmux binary through PATH or an environment
# variable, and parts of the suite resolve /usr/bin/tmux by absolute path on
# purpose (tests/testutil/throttle.go findSystemTmuxBinary). A container needs
# no such trick: there is no host tmux server to reach from inside it.
FROM golang:1.26

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        tmux \
        git \
        procps \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# The suite is pure Go (modernc.org/sqlite), and CI builds with cgo off.
# Matching that here keeps a container failure comparable to a CI failure.
ENV CGO_ENABLED=0

# Tests run as an ordinary user, not root. Several of them assert that an
# unreadable file or a read-only directory produces an error, and root ignores
# the permission bits that make those cases exist - so as root they fail for a
# reason that has nothing to do with the code. Measured: running as root
# turned TestAddDirectory_UnreadableFile, TestLoadCommandHooks_ReadError and
# TestInitCmd_ReadOnlyTargetDirDoesNotCreateNtmDir red on a tree that is green.
RUN useradd --create-home --uid 1000 ntm \
    && mkdir -p /work /go/pkg/mod /home/ntm/.cache/go-build \
    && chown -R ntm:ntm /work /go /home/ntm

ENV GOCACHE=/home/ntm/.cache/go-build
USER ntm
WORKDIR /work
