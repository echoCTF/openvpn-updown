SHELL:=/bin/sh
.PHONY: all build test clean

export GO111MODULE=on

# Path Related
MKFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
MKFILE_DIR := $(dir $(MKFILE_PATH))
RELEASE_DIR := ${MKFILE_DIR}/build/bin

# Version
RELEASE?=v0.1.0

# go source files, ignore vendor directory
SOURCE = $(shell find ${MKFILE_DIR} -type f -name "*.go")
TARGET = ${RELEASE_DIR}/openvpn-updown

all: test ${TARGET} ${TARGET}.obsd

${TARGET}: ${SOURCE}
	mkdir -p ${RELEASE_DIR}
	go mod tidy
	CGO_ENABLED=0 go build -a -ldflags '-s -w -extldflags "-static"' -gcflags=-G=3 -o ${TARGET} ${MKFILE_DIR}

${TARGET}.obsd: ${SOURCE}
	mkdir -p ${RELEASE_DIR}
	go mod tidy
	GOOS=openbsd CGO_ENABLED=0 go build -a -ldflags '-s -w -extldflags "-static"' -gcflags=-G=3 -o ${TARGET}.obsd ${MKFILE_DIR}

build: all

test:
	go test -gcflags=-l -cover -race ${TEST_FLAGS} -v ./...

clean:
	@rm -rf ${MKFILE_DIR}/build
