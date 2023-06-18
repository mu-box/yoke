#! /bin/bash -e

mkdir -p \
  monitor/mock \
  state/mock

mockgen github.com/mu-box/yoke/state State,Store > state/mock/mock.go
mockgen github.com/mu-box/yoke/monitor Performer > monitor/mock/mock.go
