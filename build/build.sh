#!/usr/bin/env bash
set -eu

buildDir=${PWD}/build-artifacts

function preBuild {
    echo "******** Preparing build ********"
    echo "Creating build artifacts directory '$buildDir'"
    mkdir -p $buildDir
}

function build {
    echo "******** Building ********"
    for CMD in `ls cmd`; do
        echo "building cmd/${CMD}"
        cd cmd/${CMD}
        go build -o ${buildDir}/${CMD}
        cd -
    done
}

function postBuild {
    echo "******** Collecting artifacts ********"

    echo "The $buildDir contains the following files: "
    ls -l $buildDir
}

function test {
    echo "******** Testing ********"

    # on amd64, we run extended tests (memory sanitizer & race checks)
    if [[ $(go env GOARCH) == "amd64" ]]; then
        ./build/test.sh "$@" -race
    else
        ./build/test.sh "$@"
    fi
}

function generate {
    echo "******** Generating ********"
    go generate ./...
}

preBuild
build
generate
test
postBuild

