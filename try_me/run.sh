#!/usr/bin/env bash
# Sample invocation showing the flags that replaced config.cfg/includes.conf
# (now retired - multiform reads no config files for these anymore). Edit
# the subcommand below (plan/apply/destroy/...) to try something else.
./multiform plan \
  -use-states=global_and_local \
  -preferred-state=local \
  -includes=sample1 \
  -includes=sample2 \
  -includes=sample3
