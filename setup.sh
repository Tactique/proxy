#!/bin/bash

export GOPATH=$(pwd)
go get
go install github.com/mattn/go-sqlite3/
