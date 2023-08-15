#!/bin/bash
set -euo pipefail
ENABLE_SSL=${ENABLE_SSL:-false}

# Start ovn-controller
if [[ "$ENABLE_SSL" == "false" ]]; then
  /usr/share/ovn/scripts/ovn-ctl restart_controller
else
  /usr/share/ovn/scripts/ovn-ctl --ovn-controller-ssl-key=/var/run/tls/key --ovn-controller-ssl-cert=/var/run/tls/cert --ovn-controller-ssl-ca-cert=/var/run/tls/cacert restart_controller
fi


function quit {
  /usr/share/ovn/scripts/grace_stop_ovn_controller 
  exit 0
}

trap quit EXIT

tail -f /var/log/ovn/ovn-controller.log
