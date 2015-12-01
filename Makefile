include includes.mk

SHORT_NAME := router
VERSION := 2.0.0-$(shell date "+%Y%m%d%H%M%S")

GIT_SHA := $(shell git rev-parse --short HEAD)
BUILD_TAG := git-${GIT_SHA}

# The following variables describe the containerized development environment
# and other build options
DEV_ENV_IMAGE := quay.io/deis/go-dev:0.1.0
DEV_ENV_WORK_DIR := /go/src/github.com/deis/${SHORT_NAME}
DEV_ENV_CMD := docker run --rm -v ${PWD}:${DEV_ENV_WORK_DIR} -w ${DEV_ENV_WORK_DIR} ${DEV_ENV_IMAGE}
DEV_ENV_CMD_INT := docker run -it --rm -v ${PWD}:${DEV_ENV_WORK_DIR} -w ${DEV_ENV_WORK_DIR} ${DEV_ENV_IMAGE}
LDFLAGS := "-s -X main.version=${VERSION}"
BINDIR := ./rootfs/bin

# The following variables describe the Docker image we build and where it
# is pushed to.
# If DEIS_REGISTRY is not set, try to populate it from legacy DEV_REGISTRY.
DEIS_REGISTRY ?= ${DEV_REGISTRY}
IMAGE_PREFIX ?= deis/
IMAGE := ${DEIS_REGISTRY}/${IMAGE_PREFIX}${SHORT_NAME}:${BUILD_TAG}

# The following variables describe k8s manifests we may wish to deploy
# to a running k8s cluster in the course of development.
RC := manifests/deis-${SHORT_NAME}-rc.yaml
SVC := manifests/deis-${SHORT_NAME}-service.yaml

# Allow developers to step into the containerized development environment
dev: check-docker
	${DEV_ENV_CMD_INT} bash

dev-registry: check-docker
	@docker inspect registry >/dev/null 2>&1 && docker start registry || docker run --restart="always" -d -p 5000:5000 --name registry registry:0.9.1
	@echo
	@echo "To use a local registry for Deis development:"
	@echo "    export DEIS_REGISTRY=`docker-machine ip $$(docker-machine active 2>/dev/null) 2>/dev/null || echo $(HOST_IPADDR) `:5000"

# Containerized dependency resolution
bootstrap: check-docker
	${DEV_ENV_CMD} glide up

# Containerized build of the binary
build: check-docker check-registry
	mkdir -p ${BINDIR}
	${DEV_ENV_CMD} make binary-build
	docker build --rm -t ${IMAGE} rootfs

# Builds the binary-- this should only be executed within the
# containerized development environment.
binary-build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ${BINDIR}/${SHORT_NAME} -a -installsuffix cgo -ldflags ${LDFLAGS} ${SHORT_NAME}.go

clean: check-docker
	docker rmi ${IMAGE}

full-clean: check-docker
	docker images -q ${DEIS_REGISTRY}/${IMAGE_PREFIX}${SHORT_NAME} | xargs docker rmi -f

dev-release: push set-image

push: check-docker check-registry build
	docker push ${IMAGE}

set-image:
	sed "s#\(image:\) .*#\1 ${IMAGE}#" manifests/deis-${SHORT_NAME}-rc.yaml > manifests/deis-${SHORT_NAME}-rc.tmp.yaml

deploy: check-kubectl dev-release
	@kubectl describe rc deis-${SHORT_NAME} --namespace=deis >/dev/null 2>&1; \
	if [ $$? -eq 0 ]; then \
		kubectl delete rc deis-${SHORT_NAME} --namespace=deis; \
		kubectl create -f manifests/deis-${SHORT_NAME}-rc.tmp.yaml; \
	else \
		kubectl create -f manifests/deis-${SHORT_NAME}-rc.tmp.yaml; \
	fi

examples:
	kubectl create -f manifests/examples.yaml
