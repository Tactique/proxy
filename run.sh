#!/bin/bash

pushd $(dirname $0) > /dev/null

pkill tactique_proxy
GOPATH=$(pwd) go build -o tactique_proxy proxy.go
./tactique_proxy -logpath=$(pwd)/proxy.log > /dev/null &

popd > /dev/null
