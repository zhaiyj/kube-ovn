#!/bin/bash
set -euo pipefail
shopt -s expand_aliases
alias ovn-ctl='/usr/share/ovn/scripts/ovn-ctl'
ovn-ctl status_controller
