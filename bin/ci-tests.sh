#!/bin/bash

TEST_TIMEOUT=15m

PKGS="$(go list ./...)"

# Support passing custom flags (-json, etc.)
OPTS="$@"
if [[ -z "$OPTS" ]]; then
	OPTS="-race -count=1 -v"
fi

export PKG_PATH=${GOPATH}/src/github.com/TykTechnologies/tyk

# exit on non-zero exit from go test/vet
set -e

# build Go-plugin used in tests
echo "Building go plugin"

tags=""
if [[ $FIPS == "1" ]]; then
    tags="-tags 'boringcrypto'"
fi

go build ${tags} -race -o ./test/goplugins/goplugins.so -buildmode=plugin ./test/goplugins

for pkg in ${PKGS}; do
    tags=""
    if [[ $FIPS == "1" ]]; then
        tags="-tags 'boringcrypto'"
    fi
    if [[ ${pkg} == *"goplugin" ]]; then
        tags="-tags 'goplugin'"

        if [[ $FIPS == "1" ]]; then
            tags="-tags 'goplugin,boringcrypto'"
        fi

    fi

    coveragefile=`echo "$pkg" | awk -F/ '{print $NF}'`

    echo go test ${OPTS} -timeout ${TEST_TIMEOUT} -coverprofile=${coveragefile}.cov ${pkg} ${tags}
    TYK_LOGLEVEL=debug go test ${OPTS} -timeout ${TEST_TIMEOUT} -coverprofile=${coveragefile}.cov ${pkg} ${tags}
done

# run rpc tests separately
rpc_tests='SyncAPISpecsRPC|OrgSessionWithRPCDown'
TYK_LOGLEVEL=debug go test -count=1 -timeout ${TEST_TIMEOUT} -v -coverprofile=gateway-rpc.cov github.com/TykTechnologies/tyk/gateway -p 1 -run '"'${rpc_tests}'"'
