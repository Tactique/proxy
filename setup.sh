#!/bin/bash

ME=$(basename $0)

function usage() {
	echo "Usage: ${ME} [deps]"
}

function go_get_install() {
	export GOPATH=$(pwd)
	go get
	go install github.com/mattn/go-sqlite3/
}

function main() {
	if [ $# -lt 1 ]; then
		go_get_install
	else
		if [ $1 == 'deps' ]; then
			echo "no deps, ok"
		else
			usage
		fi
	fi
}

main $@
